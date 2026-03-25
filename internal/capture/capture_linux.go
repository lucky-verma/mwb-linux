//go:build linux

// Package capture monitors the cursor position and evdev input events,
// forwarding them as MWB protocol packets when the cursor crosses a screen edge.
package capture

import (
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

	"github.com/bketelsen/mwb/internal/input"
	"github.com/bketelsen/mwb/internal/network"
	"github.com/bketelsen/mwb/internal/protocol"
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
	lastSwitch    time.Time // debounce outgoing switches
	switchSent    time.Time // when we last sent switch packets
	lastActivated time.Time // when cursor last arrived on this machine
	remoteX       int32     // virtual cursor position on remote (pixels)
	remoteY       int32     // virtual cursor position on remote (pixels)
	remoteW       int32     // detected remote screen width
	remoteH       int32     // detected remote screen height
	edgeY         int32     // Y position where cursor left local screen
	prevX         int32     // previous cursor X for direction detection
	canSwitch     bool      // true once cursor has been away from edge since activation
}

// New creates a new input capturer.
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
	defer c.mu.Unlock()
	if c.active != active {
		slog.Info("cursor ownership changed", "active", active)
	}
	wasActive := c.active
	c.active = active
	if active && !wasActive {
		c.switchSent = time.Time{}
		c.lastActivated = time.Now()
		c.canSwitch = false // must move away from edge first
		enableXinput()      // synchronous — must complete before mouse works
	}
}

// IsActive returns true if cursor is on this machine.
func (c *Capturer) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
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

// Stop signals the capturer to stop.
func (c *Capturer) Stop() {
	close(c.stopCh)
}

// Run starts edge detection polling and evdev monitoring.
func (c *Capturer) Run() error {
	go c.pollCursorEdge()

	devices, err := findInputDevices()
	if err != nil {
		return fmt.Errorf("find input devices: %w", err)
	}
	slog.Info("found input devices", "count", len(devices))
	for _, d := range devices {
		go c.monitorDevice(d)
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
				entryX := int32(0) // enter from left of remote
				if c.edgeSide == "left" {
					entryX = 65535 // enter from right of remote
				}

				c.mu.Lock()
				c.active = false
				c.switchSent = time.Now()
				c.edgeY = y
				// Set virtual cursor at the entry point (in remote pixel space)
				if c.edgeSide == "left" {
					c.remoteX = c.remoteW - 1
				} else {
					c.remoteX = 0
				}
				c.remoteY = int32(float64(y) / float64(c.screen.Height) * float64(c.remoteH))
				c.mu.Unlock()

				// Disable local input in X11 (synchronous — only takes ~2ms)
				disableXinput()

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

var cachedDisplay string

func getDisplay() string {
	if cachedDisplay != "" {
		return cachedDisplay
	}
	d := os.Getenv("DISPLAY")
	if d != "" {
		cachedDisplay = d
		return d
	}
	entries, err := os.ReadDir("/proc")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			env, err := os.ReadFile(fmt.Sprintf("/proc/%s/environ", e.Name()))
			if err != nil {
				continue
			}
			for _, kv := range strings.Split(string(env), "\x00") {
				if strings.HasPrefix(kv, "DISPLAY=") {
					cachedDisplay = strings.TrimPrefix(kv, "DISPLAY=")
					return cachedDisplay
				}
			}
		}
	}
	cachedDisplay = ":1"
	return cachedDisplay
}

func getCursorPosition() (x, y int32, err error) {
	cmd := exec.Command("xdotool", "getmouselocation")
	cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, fmt.Errorf("xdotool: %w", err)
	}
	var ix, iy int
	_, err = fmt.Sscanf(string(out), "x:%d y:%d", &ix, &iy)
	if err != nil {
		return 0, 0, err
	}
	return int32(ix), int32(iy), nil
}

// getXinputIDs finds xinput device IDs for Razer/Wooting devices.
func getXinputIDs() []int {
	cmd := exec.Command("xinput", "list", "--id-only")
	cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	// Get full list with names to match
	cmd2 := exec.Command("xinput", "list")
	cmd2.Env = append(os.Environ(), "DISPLAY="+getDisplay())
	out2, _ := cmd2.Output()
	lines := strings.Split(string(out2), "\n")

	_ = out
	var ids []int
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "razer") || strings.Contains(lower, "wooting") {
			// Extract id=N
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

// disableXinput disables Razer/Wooting devices in X11 so only we get events.
func disableXinput() {
	for _, id := range getXinputIDs() {
		cmd := exec.Command("xinput", "disable", strconv.Itoa(id))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		cmd.Run()
	}
	slog.Info("disabled Razer/Wooting xinput devices")
}

// enableXinput re-enables Razer/Wooting devices in X11.
func enableXinput() {
	for _, id := range getXinputIDs() {
		cmd := exec.Command("xinput", "enable", strconv.Itoa(id))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		cmd.Run()
	}
	slog.Info("enabled Razer/Wooting xinput devices")
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

func (c *Capturer) monitorDevice(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	slog.Debug("monitoring device", "path", path)
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

	// Check if virtual cursor hit the return edge (opposite of edgeSide)
	switchBack := false
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
		cmd := exec.Command("xdotool", "mousemove", "--",
			fmt.Sprintf("%d", entryX),
			fmt.Sprintf("%d", entryY))
		cmd.Env = append(os.Environ(), "DISPLAY="+getDisplay())
		cmd.Run()

		enableXinput()
		c.mu.Lock()
		return
	}

	// Send absolute mouse position to remote
	absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
	absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
	c.sendMouseLocked(absX, absY, 0, protocol.WM_MOUSEMOVE)
}

// hotkey state tracking
var hotkeyCtrl, hotkeyAlt bool

func (c *Capturer) handleKey(ev inputEvent) {
	// Track Ctrl+Alt for hotkey
	if ev.Code == 29 || ev.Code == 97 {
		hotkeyCtrl = ev.Value == 1
	}
	if ev.Code == 56 || ev.Code == 100 {
		hotkeyAlt = ev.Value == 1
	}
	// Ctrl+Alt+Right = force return to Ubuntu
	if ev.Code == 106 && ev.Value == 1 && hotkeyCtrl && hotkeyAlt {
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
				if ev.Value == 1 {
					flags = protocol.WM_LBUTTONDOWN
				} else if ev.Value == 0 {
					flags = protocol.WM_LBUTTONUP
				} else {
					return
				}
			case input.BTN_RIGHT:
				if ev.Value == 1 {
					flags = protocol.WM_RBUTTONDOWN
				} else if ev.Value == 0 {
					flags = protocol.WM_RBUTTONUP
				} else {
					return
				}
			case input.BTN_MIDDLE:
				if ev.Value == 1 {
					flags = protocol.WM_MBUTTONDOWN
				} else if ev.Value == 0 {
					flags = protocol.WM_MBUTTONUP
				} else {
					return
				}
			}
			c.sendMouse(0, 0, 0, flags)
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
