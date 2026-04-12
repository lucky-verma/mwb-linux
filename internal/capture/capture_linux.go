//go:build linux

// Package capture monitors the cursor position and evdev input events,
// forwarding them as MWB protocol packets when the cursor crosses a screen edge.
package capture

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const (
	evKey = 0x01
	evRel = 0x02

	relX     = 0x00
	relY     = 0x01
	relWheel = 0x08

	inputEventSize = 24

	// Default remote screen dimensions — auto-detected from incoming packets
	defaultRemoteWidth  = 1920
	defaultRemoteHeight = 1080
)

type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

// ScreenInfo holds screen dimensions.
type ScreenInfo struct {
	Width  int32
	Height int32
}

// Capturer monitors input and forwards events to the remote MWB host.
type Capturer struct {
	conn          *network.Conn
	screen        ScreenInfo
	active        bool   // true = cursor is on this machine
	edgeSide      string // "left" or "right"
	mu            sync.Mutex
	stopCh        chan struct{}
	wg            sync.WaitGroup // tracks all goroutines for clean Stop()
	deviceFiles   []*os.File     // open /dev/input/event* fds — closed on Stop() to unblock f.Read
	lastSwitch    time.Time      // debounce outgoing switches
	switchSent    time.Time      // when we last sent switch packets
	lastActivated time.Time      // when cursor last arrived on this machine
	remoteX       int32          // virtual cursor position on remote (pixels)
	remoteY       int32          // virtual cursor position on remote (pixels)
	remoteW       int32          // detected remote screen width
	remoteH       int32          // detected remote screen height
	edgeY         int32          // Y position where cursor left local screen
	canSwitch         bool       // true once cursor has been away from edge since activation
	canReturn         bool       // true once cursor has moved away from the remote return edge
	hotkeyCtrl        bool       // tracks Ctrl key state for hotkey detection
	hotkeyAlt         bool       // tracks Alt key state for hotkey detection
	disabledXinputIDs []int      // device IDs we disabled — re-enable same set to avoid TOCTOU
}

// New creates a new input capturer.
// Does NOT call enableXinput — Stop() on the previous Capturer already
// re-enables any devices it disabled. Calling xinput enable unconditionally
// here can corrupt the attachment state of floating slave devices.
func New(conn *network.Conn, screen ScreenInfo, edgeSide string) *Capturer {
	return &Capturer{
		conn:      conn,
		screen:    screen,
		active:    true,
		edgeSide:  edgeSide,
		stopCh:    make(chan struct{}),
		remoteW:   defaultRemoteWidth,
		remoteH:   defaultRemoteHeight,
		canSwitch: true, // allow first switch immediately
	}
}

// SetActive sets whether this machine currently owns the cursor.
func (c *Capturer) SetActive(active bool) {
	c.mu.Lock()
	if c.active != active {
		slog.Info("cursor ownership changed", "active", active)
	}
	wasActive := c.active
	c.active = active
	shouldEnable := active && !wasActive
	if shouldEnable {
		c.switchSent = time.Time{}
		c.lastActivated = time.Now()
		c.canSwitch = false // must move away from local edge before next outbound switch
		c.canReturn = false // must move away from remote edge before next return switch
	}
	c.mu.Unlock()
	// enableXinput acquires c.mu internally — must be called after unlock.
	// Calling it under the lock caused a deadlock that froze all goroutines.
	if shouldEnable {
		c.enableXinput()
	}
}

// IsActive returns true if cursor is on this machine.
func (c *Capturer) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// SafeEntryPosition returns a cursor position 100px inside from the switch edge,
// safe to move to after MachineSwitched without immediately re-triggering the edge.
func (c *Capturer) SafeEntryPosition() (x, y int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	y = c.screen.Height / 2
	switch c.edgeSide {
	case "left":
		x = 100
	case "right":
		x = c.screen.Width - 100
	default:
		x = c.screen.Width / 2
	}
	return x, y
}

// UpdateRemoteScreen detects remote screen dimensions from incoming Mouse packets.
// Called by the handler when we receive absolute mouse coordinates from the server.
func (c *Capturer) UpdateRemoteScreen(absX, absY int32) {
	// MWB absolute coords are 0-65535. We can't directly detect resolution from them.
	// But we can detect it from the Matrix/HeartbeatEx packets or config.
	// For now, this is a placeholder — resolution comes from config or is auto-detected.
}

// SetRemoteScreen sets the remote screen dimensions.
func (c *Capturer) SetRemoteScreen(w, h int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w > 0 && h > 0 && (w != c.remoteW || h != c.remoteH) {
		c.remoteW = w
		c.remoteH = h
		slog.Info("remote screen dimensions updated", "width", w, "height", h)
	}
}

// Stop signals the capturer to stop, waits for all goroutines to exit,
// and ensures xinput devices are always re-enabled on teardown.
func (c *Capturer) Stop() {
	close(c.stopCh)
	// Close all device fds to unblock any goroutines stuck in f.Read().
	// Without this, monitorDevice goroutines block indefinitely and accumulate
	// across reconnect cycles (35 devices × N reconnects = goroutine storm).
	c.mu.Lock()
	for _, f := range c.deviceFiles {
		_ = f.Close()
	}
	c.mu.Unlock()
	c.wg.Wait()
	// Only re-enable if WE disabled them — avoids calling xinput enable on
	// floating/unmanaged devices which can corrupt their attachment state.
	c.mu.Lock()
	hasDisabled := len(c.disabledXinputIDs) > 0
	c.mu.Unlock()
	if hasDisabled {
		c.enableXinput()
	}
}

// Run starts edge detection polling and evdev monitoring.
// Validates all preconditions before starting any goroutines.
func (c *Capturer) Run() error {
	devices, err := findInputDevices()
	if err != nil {
		return fmt.Errorf("find input devices: %w", err)
	}
	slog.Info("found input devices", "count", len(devices))

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.pollCursorEdge()
	}()
	for _, d := range devices {
		f, err := os.Open(d)
		if err != nil {
			continue
		}
		c.mu.Lock()
		c.deviceFiles = append(c.deviceFiles, f)
		c.mu.Unlock()
		c.wg.Add(1)
		go func(file *os.File) {
			defer c.wg.Done()
			c.monitorDevice(file)
		}(f)
	}
	return nil
}

// pollCursorEdge checks the actual cursor position and triggers switches.
func (c *Capturer) pollCursorEdge() {
	slog.Info("edge polling started", "edge", c.edgeSide, "screenWidth", c.screen.Width)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	errCount := 0
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			if !c.IsActive() {
				continue
			}
			c.mu.Lock()
			// canSwitch gate handles loop prevention — no time-based cooldown needed
			c.mu.Unlock()
			x, y, err := getCursorPosition()
			if err != nil {
				errCount++
				if errCount <= 3 {
					slog.Warn("getCursorPosition failed", "err", err, "count", errCount)
				}
				continue
			}
			if errCount > 0 {
				errCount = 0
			}

			// Track whether cursor has been away from the edge since activation
			// This prevents loops: cursor must move inward first, then back to edge
			c.mu.Lock()
			edgeZone := int32(20) // pixels from edge — must move this far inward to re-arm
			switch c.edgeSide {
			case "left":
				if x > edgeZone {
					c.canSwitch = true
				}
			case "right":
				if x < c.screen.Width-edgeZone {
					c.canSwitch = true
				}
			}
			canSwitch := c.canSwitch
			c.mu.Unlock()

			switched := false
			if canSwitch {
				switch c.edgeSide {
				case "left":
					if x <= 1 {
						switched = true
					}
				case "right":
					if x >= c.screen.Width-1 {
						switched = true
					}
				}
			}

			if switched {
				now := time.Now()
				if now.Sub(c.lastSwitch) < 100*time.Millisecond {
					continue
				}
				c.lastSwitch = now

				slog.Info("screen edge hit, switching to remote", "edge", c.edgeSide, "x", x, "y", y)

				// Map local Y to remote entry point (proportional)
				entryY := int32(float64(y) / float64(c.screen.Height) * 65535)
				// Enter 200px inside the remote screen, not at the literal edge.
				// Entering at exactly 0 or 65535 triggers Windows MWB's own edge
				// detection immediately, bouncing the cursor straight back.
				// 200px margin ≈ 200/1920 * 65535 ≈ 6826 units from the edge.
				const edgeMargin = int32(6826)
				entryX := edgeMargin // enter from left of remote, slightly inside
				if c.edgeSide == "left" {
					entryX = 65535 - edgeMargin // enter from right of remote, slightly inside
				}

				c.mu.Lock()
				c.active = false
				c.switchSent = time.Now()
				c.edgeY = y
				// Set virtual cursor offset from the return edge to prevent jitter bounce.
				// Entry is 200px from the return edge — gives room for mouse momentum.
				if c.edgeSide == "left" {
					c.remoteX = c.remoteW - 200
				} else {
					c.remoteX = 200
				}
				c.remoteY = int32(float64(y) / float64(c.screen.Height) * float64(c.remoteH))
				c.canReturn = false // must move away from return edge first
				c.mu.Unlock()

				// Disable local input in X11 (synchronous — only takes ~2ms)
				c.disableXinput()

				// Send mouse burst to the entry position on remote
				// Multiple packets help Windows MWB register the switch reliably
				conn := c.conn
				go func() {
					for i := 0; i < 5; i++ {
						mouse := &protocol.Packet{
							Type: protocol.Mouse,
							Src:  conn.MachineID,
							Des:  conn.RemoteID,
						}
						mouse.Mouse.X = entryX
						mouse.Mouse.Y = entryY
						mouse.Mouse.DwFlags = protocol.WM_MOUSEMOVE
						_ = conn.SendPacket(mouse)
						time.Sleep(5 * time.Millisecond)
					}
				}()
			}
		}
	}
}

var (
	displayOnce   sync.Once
	cachedDisplay string
)

// DetectDisplay finds the active X11 display and XAUTHORITY, caches the result,
// and sets XAUTHORITY in the process environment if missing.
// Detection order: DISPLAY env var → loginctl session query → X11 socket scan → ":0".
// Safe to call from multiple goroutines; detection runs exactly once.
func DetectDisplay() string {
	return getDisplay()
}

func getDisplay() string {
	displayOnce.Do(func() {
		detect()
	})
	return cachedDisplay
}

func detect() {

	// 1. Check environment variable (explicit override)
	d := os.Getenv("DISPLAY")

	// 2. Ask loginctl for the active graphical session's display
	if d == "" {
		d = detectDisplayFromLoginctl()
	}

	// 3. Scan X11 sockets as last resort
	if d == "" {
		d = detectDisplayFromSockets()
	}

	// 4. Final fallback
	if d == "" {
		d = ":0"
	}

	cachedDisplay = d
	// Set in process environment so all child commands (xrandr, xdotool, xinput, xclip) inherit it
	if err := os.Setenv("DISPLAY", d); err != nil {
		slog.Warn("failed to set DISPLAY env", "err", err)
	}
	slog.Info("X11 display detected", "display", d)

	// Also ensure XAUTHORITY is set — xdotool/xinput/xclip need it when running as root
	detectAndSetXauthority(d)
}

// detectDisplayFromLoginctl queries loginctl for an active X11 session.
func detectDisplayFromLoginctl() string {
	out, err := exec.Command("loginctl", "list-sessions", "--no-legend").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		sid := fields[0]
		display, err := exec.Command("loginctl", "show-session", sid, "-p", "Display", "--value").Output()
		if err != nil {
			continue
		}
		d := strings.TrimSpace(string(display))
		if d != "" {
			return d
		}
	}
	return ""
}

// detectDisplayFromSockets checks /tmp/.X11-unix/ for active X server sockets.
func detectDisplayFromSockets() string {
	entries, err := os.ReadDir("/tmp/.X11-unix")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "X") {
			return ":" + strings.TrimPrefix(name, "X")
		}
	}
	return ""
}

// detectAndSetXauthority finds the Xauthority file for the given display
// and sets XAUTHORITY in the process environment if not already set.
func detectAndSetXauthority(display string) {
	if os.Getenv("XAUTHORITY") != "" {
		return
	}
	// Common GDM/SDDM paths for UID 1000+ users
	entries, _ := os.ReadDir("/run/user")
	for _, e := range entries {
		// Try GDM path first, then generic .Xauthority
		candidates := []string{
			fmt.Sprintf("/run/user/%s/gdm/Xauthority", e.Name()),
			fmt.Sprintf("/run/user/%s/.Xauthority", e.Name()),
		}
		for _, path := range candidates {
			if _, err := os.Stat(path); err == nil {
				if err := os.Setenv("XAUTHORITY", path); err != nil {
					slog.Warn("failed to set XAUTHORITY env", "err", err)
				} else {
					slog.Info("XAUTHORITY auto-detected", "path", path)
				}
				return
			}
		}
	}
	// Try home directory fallback
	if home := os.Getenv("HOME"); home != "" {
		path := filepath.Join(home, ".Xauthority")
		if _, err := os.Stat(path); err == nil {
			if err := os.Setenv("XAUTHORITY", path); err != nil {
				slog.Warn("failed to set XAUTHORITY env", "err", err)
			} else {
				slog.Info("XAUTHORITY auto-detected", "path", path)
			}
		}
	}
}

func getCursorPosition() (x, y int32, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xdotool", "getmouselocation")
	cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
	out, err := cmd.Output()
	if err != nil {
		return -1, -1, fmt.Errorf("xdotool: %w", err)
	}
	var ix, iy int
	if _, err = fmt.Sscanf(string(out), "x:%d y:%d", &ix, &iy); err != nil {
		// Return sentinel -1,-1 to distinguish parse failure from cursor at origin (0,0)
		return -1, -1, fmt.Errorf("xdotool parse: %w", err)
	}
	return int32(ix), int32(iy), nil
}

// getXinputIDs finds xinput device IDs for Razer/Wooting devices.
func getXinputIDs() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xinput", "list")
	cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var ids []int
	for _, line := range strings.Split(string(out), "\n") {
		lower := strings.ToLower(line)
		// Only manage attached slaves (↳ prefix) — skip floating slaves (∼ prefix).
		// Floating slaves are already detached from the master and don't inject
		// events into X11; disabling them serves no purpose and breaks re-enable.
		if strings.Contains(line, "[floating slave]") {
			continue
		}
		if strings.Contains(lower, "razer") || strings.Contains(lower, "wooting") {
			if idx := strings.Index(line, "id="); idx >= 0 {
				numStr := ""
				for _, ch := range line[idx+3:] {
					if ch >= '0' && ch <= '9' {
						numStr += string(ch)
					} else {
						break
					}
				}
				if id, err := strconv.Atoi(numStr); err == nil {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// disableXinput disables Razer/Wooting devices and caches which IDs were disabled
// so enableXinput re-enables the exact same set (avoids TOCTOU if devices change).
func (c *Capturer) disableXinput() {
	ids := getXinputIDs()
	c.mu.Lock()
	c.disabledXinputIDs = ids
	c.mu.Unlock()
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "xinput", "disable", strconv.Itoa(id))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		_ = cmd.Run()
		cancel()
	}
	slog.Info("disabled Razer/Wooting xinput devices", "count", len(ids))
}

// enableXinput re-enables the exact device IDs that were disabled by disableXinput.
// Also scans for any Razer/Wooting devices that are attached-but-disabled from a
// previous broken session (e.g. disableXinput ran but enableXinput never did because
// the connection dropped). Only touches attached slaves — never floating devices.
func (c *Capturer) enableXinput() {
	c.mu.Lock()
	ids := c.disabledXinputIDs
	c.disabledXinputIDs = nil
	c.mu.Unlock()

	// Always include currently-disabled attached devices to recover from prior
	// broken sessions — idempotent for already-enabled devices.
	current := getXinputIDs()
	merged := make(map[int]struct{}, len(ids)+len(current))
	for _, id := range ids {
		merged[id] = struct{}{}
	}
	for _, id := range current {
		merged[id] = struct{}{}
	}

	for id := range merged {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "xinput", "enable", strconv.Itoa(id))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		_ = cmd.Run()
		cancel()
	}
	slog.Info("enabled Razer/Wooting xinput devices", "count", len(merged))
}

func findInputDevices() ([]string, error) {
	entries, err := os.ReadDir("/dev/input")
	if err != nil {
		return nil, err
	}
	var devices []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "event") {
			devices = append(devices, filepath.Join("/dev/input", e.Name()))
		}
	}
	return devices, nil
}

func (c *Capturer) monitorDevice(f *os.File) {
	defer f.Close() //nolint:errcheck
	slog.Debug("monitoring device", "path", f.Name())
	buf := make([]byte, inputEventSize*32)
	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		n, err := f.Read(buf)
		if err != nil {
			return
		}

		for off := 0; off+inputEventSize <= n; off += inputEventSize {
			ev := parseEvent(buf[off : off+inputEventSize])
			c.handleEvent(ev)
		}
	}
}

func parseEvent(buf []byte) inputEvent {
	return inputEvent{
		Sec:   int64(binary.LittleEndian.Uint64(buf[0:8])),
		Usec:  int64(binary.LittleEndian.Uint64(buf[8:16])),
		Type:  binary.LittleEndian.Uint16(buf[16:18]),
		Code:  binary.LittleEndian.Uint16(buf[18:20]),
		Value: int32(binary.LittleEndian.Uint32(buf[20:24])),
	}
}

func (c *Capturer) handleEvent(ev inputEvent) {
	if c.IsActive() {
		return
	}
	// Suppress during switch grace period
	c.mu.Lock()
	grace := !c.switchSent.IsZero() && time.Since(c.switchSent) < 100*time.Millisecond
	c.mu.Unlock()
	if grace {
		return
	}

	switch ev.Type {
	case evRel:
		c.handleRel(ev)
	case evKey:
		c.handleKey(ev)
	}
}

// applyAcceleration scales raw evdev deltas to approximate libinput's flat profile.
// libinput flat profile: output = input * (1 + speed_setting)
// With accel speed 0.766: multiplier = 1.766
// We round to ~2x which is a good general default.
const accelMultiplier = 2.0

func applyAcceleration(delta int32) int32 {
	scaled := float64(delta) * accelMultiplier
	if scaled > 0 && scaled < 1 {
		return 1
	}
	if scaled < 0 && scaled > -1 {
		return -1
	}
	return int32(scaled)
}

func (c *Capturer) handleRel(ev inputEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch ev.Code {
	case relX:
		c.remoteX += applyAcceleration(ev.Value)
		if c.remoteX < 0 {
			c.remoteX = 0
		}
		if c.remoteX > c.remoteW {
			c.remoteX = c.remoteW
		}
	case relY:
		c.remoteY += applyAcceleration(ev.Value)
		if c.remoteY < 0 {
			c.remoteY = 0
		}
		if c.remoteY > c.remoteH {
			c.remoteY = c.remoteH
		}
	case relWheel:
		c.sendMouseLocked(0, 0, ev.Value*120, protocol.WM_MOUSEWHEEL)
		return
	default:
		return
	}

	// canReturn gate: must move 200px away from return edge before allowing return.
	// This prevents jitter/momentum from the initial switch from bouncing back.
	returnZone := int32(200)
	switch c.edgeSide {
	case "left":
		if c.remoteX < c.remoteW-returnZone {
			c.canReturn = true
		}
	case "right":
		if c.remoteX > returnZone {
			c.canReturn = true
		}
	}

	// Check if virtual cursor hit the return edge (opposite of edgeSide)
	switchBack := false
	if c.canReturn {
		switch c.edgeSide {
		case "left":
			// We switched to remote via left edge, return via right edge of remote
			if c.remoteX >= c.remoteW-1 {
				switchBack = true
			}
		case "right":
			if c.remoteX <= 0 {
				switchBack = true
			}
		}
	}

	// Log virtual position periodically for debugging
	if c.remoteX%200 == 0 || switchBack {
		slog.Debug("virtual cursor", "x", c.remoteX, "y", c.remoteY, "switchBack", switchBack)
	}

	if switchBack {
		remY := c.remoteY
		remH := c.remoteH
		slog.Info("remote edge hit — switching back to Ubuntu", "remoteX", c.remoteX, "remoteY", remY)
		c.active = true
		c.switchSent = time.Time{}
		c.lastActivated = time.Now()
		c.canSwitch = false // block re-trigger until cursor moves away from edge
		c.mu.Unlock()

		// Move cursor away from edge SYNCHRONOUSLY before enabling xinput
		var entryX int32
		if c.edgeSide == "left" {
			entryX = 100
		} else {
			entryX = c.screen.Width - 100
		}
		entryY := int32(float64(remY) / float64(remH) * float64(c.screen.Height))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "xdotool", "mousemove", "--",
			fmt.Sprintf("%d", entryX),
			fmt.Sprintf("%d", entryY))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		_ = cmd.Run()
		cancel()

		c.enableXinput()
		c.mu.Lock()
		return
	}

	// Send absolute mouse position to remote
	absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
	absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
	c.sendMouseLocked(absX, absY, 0, protocol.WM_MOUSEMOVE)
}

func (c *Capturer) handleKey(ev inputEvent) {
	// Track Ctrl+Alt for hotkey — guarded by c.mu via handleEvent → monitorDevice path.
	// Left/right Ctrl (29, 97) and Left/right Alt (56, 100).
	if ev.Code == 29 || ev.Code == 97 {
		c.hotkeyCtrl = ev.Value == 1
	}
	if ev.Code == 56 || ev.Code == 100 {
		c.hotkeyAlt = ev.Value == 1
	}
	// Ctrl+Alt+Right = force return to Ubuntu
	if ev.Code == 106 && ev.Value == 1 && c.hotkeyCtrl && c.hotkeyAlt {
		if !c.IsActive() {
			slog.Info("hotkey Ctrl+Alt+Right: returning to Ubuntu")
			c.SetActive(true)
			return
		}
	}

	// Mouse buttons
	if ev.Code >= 0x110 && ev.Code <= 0x112 {
		if !c.IsActive() {
			var flags int32
			switch ev.Code {
			case input.BTN_LEFT:
				switch ev.Value {
				case 1:
					flags = protocol.WM_LBUTTONDOWN
				case 0:
					flags = protocol.WM_LBUTTONUP
				default:
					return
				}
			case input.BTN_RIGHT:
				switch ev.Value {
				case 1:
					flags = protocol.WM_RBUTTONDOWN
				case 0:
					flags = protocol.WM_RBUTTONUP
				default:
					return
				}
			case input.BTN_MIDDLE:
				switch ev.Value {
				case 1:
					flags = protocol.WM_MBUTTONDOWN
				case 0:
					flags = protocol.WM_MBUTTONUP
				default:
					return
				}
			}
			// Use current virtual cursor position so clicks register at the
			// correct location on Windows, not always at top-left (0,0).
			c.mu.Lock()
			absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
			absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
			c.mu.Unlock()
			c.sendMouse(absX, absY, 0, flags)
		}
		return
	}

	// Keyboard
	if ev.Value == 2 {
		return // skip repeat
	}
	if c.IsActive() {
		return
	}

	vk, ok := input.KeyCodeToVK(ev.Code)
	if !ok {
		return
	}

	var dwFlags int32
	if ev.Value == 0 {
		dwFlags = protocol.LLKHF_UP
	}

	pkt := &protocol.Packet{
		Type: protocol.Keyboard,
		Src:  c.conn.MachineID,
		Des:  c.conn.RemoteID,
	}
	pkt.Keyboard.WVk = vk
	pkt.Keyboard.DwFlags = dwFlags

	if err := c.conn.SendPacket(pkt); err != nil {
		slog.Debug("send keyboard failed", "err", err)
	}
}

func (c *Capturer) sendMouse(x, y, wheelDelta, flags int32) {
	pkt := &protocol.Packet{
		Type: protocol.Mouse,
		Src:  c.conn.MachineID,
		Des:  c.conn.RemoteID,
	}
	pkt.Mouse.X = x
	pkt.Mouse.Y = y
	pkt.Mouse.WheelDelta = wheelDelta
	pkt.Mouse.DwFlags = flags

	if err := c.conn.SendPacket(pkt); err != nil {
		slog.Debug("send mouse failed", "err", err)
	}
}

// sendMouseLocked sends a mouse packet (caller must hold c.mu).
func (c *Capturer) sendMouseLocked(x, y, wheelDelta, flags int32) {
	pkt := &protocol.Packet{
		Type: protocol.Mouse,
		Src:  c.conn.MachineID,
		Des:  c.conn.RemoteID,
	}
	pkt.Mouse.X = x
	pkt.Mouse.Y = y
	pkt.Mouse.WheelDelta = wheelDelta
	pkt.Mouse.DwFlags = flags

	if err := c.conn.SendPacket(pkt); err != nil {
		slog.Debug("send mouse failed", "err", err)
	}
}
