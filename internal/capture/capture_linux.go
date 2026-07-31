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
	"strings"
	"sync"
	"syscall"
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

	// Default scaling applied to raw evdev deltas. Approximates libinput's flat
	// profile (speed 0.766 → ~1.766, rounded to 2). Overridable via config.
	defaultAccelMultiplier = 2.0

	// NextMachine landing requests use MWB's 0-65535 absolute coordinate space.
	// A legitimate reclaim must land on the local edge that is physically shared
	// with the remote. The margin is intentionally broad enough for MWB's tiny
	// 2px jump plus rounding, but narrow enough to reject the opposite far edge.
	reclaimEdgeMargin = 8192

	// remoteReturnMargin is in remote-screen pixels, not 0-65535 wire units.
	// It must be smaller than the 200px remote entry offset, otherwise a
	// MachineSwitched notification at the entry position can bounce straight
	// back before the cursor reaches the real shared edge.
	remoteReturnMargin = 64
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
	grabMu        sync.Mutex                 // serializes isolation changes; never held with mu
	setGrabFn     func(*os.File, bool) error // test seam; nil uses the real ioctl
	findDevicesFn func() ([]string, error)   // test seam; nil scans /dev/input
	stopCh        chan struct{}
	wg            sync.WaitGroup            // tracks all goroutines for clean Stop()
	devices       map[string]*trackedDevice // open /dev/input/event* fds, keyed by path
	stopped       bool                      // set by Stop(); blocks new goroutines racing wg.Wait
	lastSwitch    time.Time                 // debounce outgoing switches
	switchSent    time.Time                 // when we last sent switch packets
	lastActivated time.Time                 // when cursor last arrived on this machine
	remoteX       int32                     // virtual cursor position on remote (pixels)
	remoteY       int32                     // virtual cursor position on remote (pixels)
	remoteW       int32                     // detected remote screen width
	remoteH       int32                     // detected remote screen height
	accelMult     float64                   // scaling applied to raw evdev deltas (config: accel_multiplier)
	edgeY         int32                     // Y position where cursor left local screen
	canSwitch     bool                      // true once cursor has been away from edge since activation
	canReturn     bool                      // true once cursor has moved away from the remote return edge
	hotkeyCtrl    bool                      // tracks Ctrl key state for hotkey detection
	hotkeyAlt     bool                      // tracks Alt key state for hotkey detection
}

// trackedDevice is one open /dev/input/event* node.
type trackedDevice struct {
	file    *os.File
	grab    bool // keyboard or pointer: suppress it while the cursor is remote
	grabbed bool // whether the exclusive grab is currently held
}

// New creates a new input capturer.
// Nothing needs releasing here: grabs are owned by the previous Capturer's file
// descriptors, and the kernel dropped them when those descriptors closed.
func New(conn *network.Conn, screen ScreenInfo, edgeSide string) *Capturer {
	return &Capturer{
		conn:      conn,
		screen:    screen,
		active:    true,
		edgeSide:  edgeSide,
		stopCh:    make(chan struct{}),
		remoteW:   defaultRemoteWidth,
		remoteH:   defaultRemoteHeight,
		accelMult: defaultAccelMultiplier,
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
	// applyIsolation acquires c.mu internally — must be called after unlock.
	// Calling it under the lock caused a deadlock that froze all goroutines.
	if shouldEnable {
		c.applyIsolation()
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

// AcceptsReclaim reports whether a remote NextMachine request is returning
// through the edge this Linux screen actually shares with the remote machine.
func (c *Capturer) AcceptsReclaim(requestX int32) bool {
	c.mu.Lock()
	edgeSide := c.edgeSide
	c.mu.Unlock()

	switch edgeSide {
	case "left":
		return requestX <= reclaimEdgeMargin
	case "right":
		return requestX >= 65535-reclaimEdgeMargin
	default:
		return true
	}
}

// AcceptsActivation reports whether a MachineSwitched notification is
// compatible with the edge this Linux screen shares with the remote. Windows can
// emit MachineSwitched from the remote machine's far edge if its matrix wraps or
// is rotated; that must not bring local control back.
func (c *Capturer) AcceptsActivation() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.active {
		return true
	}

	remoteW := c.remoteW
	if remoteW <= 0 {
		remoteW = defaultRemoteWidth
	}

	switch c.edgeSide {
	case "left":
		return c.remoteX >= remoteW-remoteReturnMargin
	case "right":
		return c.remoteX <= remoteReturnMargin
	default:
		return true
	}
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

// SetAccelMultiplier sets the scaling applied to raw evdev deltas before they
// move the remote cursor. Non-positive values are ignored so the default is
// never clobbered by an unset/invalid config value.
func (c *Capturer) SetAccelMultiplier(m float64) {
	if m <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accelMult = m
	slog.Info("cursor acceleration multiplier set", "accel_multiplier", m)
}

// Stop signals the capturer to stop and waits for all goroutines to exit.
//
// Closing the device descriptors is what restores local input: the kernel drops
// every EVIOCGRAB held on a descriptor as it closes. There is deliberately no
// separate release step here, because a release step is something that can be
// skipped — which is exactly how the previous xinput-based teardown could leave
// the machine with no mouse and no keyboard.
func (c *Capturer) Stop() {
	close(c.stopCh)
	// Close all device fds to unblock any goroutines stuck in f.Read().
	// Without this, monitorDevice goroutines block indefinitely and accumulate
	// across reconnect cycles (35 devices × N reconnects = goroutine storm).
	c.mu.Lock()
	c.stopped = true
	for _, d := range c.devices {
		_ = d.file.Close()
	}
	c.devices = nil
	c.mu.Unlock()
	c.wg.Wait()
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
		c.addDevice(d)
	}
	c.mu.Lock()
	targets := 0
	for _, d := range c.devices {
		if d.grab {
			targets++
		}
	}
	c.mu.Unlock()
	slog.Info("classified input devices", "tracked", len(devices), "keyboards_and_pointers", targets)
	return nil
}

// addDevice opens, classifies and starts monitoring one device node. Devices
// that cannot be opened are skipped: /dev/input entries appear before udev has
// applied permissions, and a node can vanish between listing and opening.
func (c *Capturer) addDevice(path string) {
	c.mu.Lock()
	_, known := c.devices[path]
	stopped := c.stopped
	c.mu.Unlock()
	if known || stopped {
		return
	}

	// O_NONBLOCK is what makes Stop() able to finish. Opened blocking, the fd is
	// not registered with Go's poller, a parked read(2) is not interrupted by
	// Close(), and monitorDevice only returns once that device happens to emit
	// an event — which silent nodes (power buttons, audio-jack detect) never do.
	// Stop() then waits forever in wg.Wait(). Non-blocking fds go through the
	// poller, where Close() evicts pending reads, with no added input latency.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	grabbable := isGrabTarget(f)

	c.mu.Lock()
	// Re-check under the lock: Stop() may have run, and a concurrent refresh
	// may have added this same path.
	if c.stopped {
		c.mu.Unlock()
		_ = f.Close()
		return
	}
	if _, dup := c.devices[path]; dup {
		c.mu.Unlock()
		_ = f.Close()
		return
	}
	if c.devices == nil {
		c.devices = make(map[string]*trackedDevice)
	}
	c.devices[path] = &trackedDevice{file: f, grab: grabbable}
	// wg.Add must happen under the same lock that guards c.stopped, otherwise
	// it can race an in-progress wg.Wait() in Stop().
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.wg.Done()
		c.monitorDevice(f)
	}()
}

// refreshDevices brings the tracked set in line with what is actually present
// in /dev/input.
//
// Input devices come and go while mwb runs: wireless receivers re-enumerate on
// wake, hubs re-attach, users plug in a second mouse mid-session. A set captured
// once at startup goes stale, and an untracked device is neither forwarded nor
// grabbed — its motion would leak straight to the local display while the cursor
// is on the remote machine.
func (c *Capturer) refreshDevices() {
	discover := c.findDevicesFn
	if discover == nil {
		discover = findInputDevices
	}
	paths, err := discover()
	if err != nil {
		slog.Warn("rescan input devices", "err", err)
		return
	}
	live := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		live[p] = struct{}{}
	}

	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	var gone []*os.File
	for path, d := range c.devices {
		if _, ok := live[path]; !ok {
			gone = append(gone, d.file)
			delete(c.devices, path)
		}
	}
	c.mu.Unlock()

	// Closing releases any grab the kernel still holds on the vanished node and
	// unblocks its monitorDevice goroutine.
	for _, f := range gone {
		_ = f.Close()
	}
	for _, p := range paths {
		c.addDevice(p)
	}
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

				// Suppress local input (synchronous — one ioctl per device)
				c.applyIsolation()

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
	// Set in process environment so all child commands (xrandr, xdotool, xclip) inherit it
	if err := os.Setenv("DISPLAY", d); err != nil {
		slog.Warn("failed to set DISPLAY env", "err", err)
	}
	slog.Info("X11 display detected", "display", d)

	// Also ensure XAUTHORITY is set — xdotool/xclip need it when running as root
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

// grabTargetFiles snapshots the keyboards and pointers currently tracked, so
// the ioctls below run without holding c.mu.
func (c *Capturer) grabTargetFiles() []*os.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	targets := make([]*os.File, 0, len(c.devices))
	for _, d := range c.devices {
		if d.grab {
			targets = append(targets, d.file)
		}
	}
	return targets
}

// applyIsolation brings the kernel grabs in line with who currently owns the
// cursor: suppressed while the cursor is on the remote machine, live while it
// is local.
//
// Both switch directions land here from different goroutines — pollCursorEdge
// for outbound switches, the network handler via SetActive for returns. The
// desired state is re-derived from c.active *inside* grabMu rather than being
// passed in by the caller. That is what stops a late release belonging to a
// finished switch-back from landing after the grab for the next switch-out and
// leaving local input live while the cursor is on the remote machine, which
// produced a window of dual-cursor movement.
func (c *Capturer) applyIsolation() {
	c.grabMu.Lock()
	defer c.grabMu.Unlock()

	suppress := !c.IsActive()
	if suppress {
		// A device plugged in while the cursor was away still has to be grabbed.
		c.refreshDevices()
	}

	changed, total := c.setIsolation(suppress)
	if changed == 0 {
		return // already in the desired state
	}
	verb := "released"
	if suppress {
		verb = "grabbed"
	}
	slog.Info(verb+" local input devices", "count", changed, "of", total)
}

// setIsolation grabs or releases only the devices not already in that state and
// reports how many changed out of how many were eligible.
//
// Skipping devices already in the target state matters: re-grabbing a device
// this process already holds returns EBUSY, which would otherwise be logged as
// a failure and undercount the result.
func (c *Capturer) setIsolation(grab bool) (changed, total int) {
	c.mu.Lock()
	var pending []*trackedDevice
	for _, d := range c.devices {
		if !d.grab {
			continue
		}
		total++
		if d.grabbed != grab {
			pending = append(pending, d)
		}
	}
	c.mu.Unlock()

	apply := c.setGrabFn
	if apply == nil {
		apply = setGrab
	}
	for _, d := range pending {
		if err := apply(d.file, grab); err != nil {
			slog.Warn("set input isolation", "device", d.file.Name(), "grab", grab, "err", err)
			continue
		}
		c.mu.Lock()
		d.grabbed = grab
		c.mu.Unlock()
		changed++
	}
	return changed, total
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

// applyAcceleration scales raw evdev deltas by mult. The Windows side does no
// acceleration of its own (absolute positioning), so mult is the only
// cursor-speed control. Sub-pixel results are clamped to ±1 so slow movements
// still register and the cursor can always reach the return edge.
func applyAcceleration(delta int32, mult float64) int32 {
	scaled := float64(delta) * mult
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
		c.remoteX += applyAcceleration(ev.Value, c.accelMult)
		if c.remoteX < 0 {
			c.remoteX = 0
		}
		if c.remoteX > c.remoteW {
			c.remoteX = c.remoteW
		}
	case relY:
		c.remoteY += applyAcceleration(ev.Value, c.accelMult)
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

		// Move cursor away from edge SYNCHRONOUSLY before releasing the grab.
		// Ordering is load-bearing: release first and in-flight physical mouse
		// motion drives the cursor straight back into the edge, bouncing the
		// switch. Move first, then hand input back.
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

		c.applyIsolation()
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
	if c.IsActive() {
		return
	}

	vk, ok := input.KeyCodeToVK(ev.Code)
	if !ok {
		return
	}

	// evdev value: 1 = press, 2 = auto-repeat, 0 = release. Forward repeats as
	// keydowns — an injected key does not auto-repeat on Windows, so a held key
	// only repeats if we resend the press (mirrors MWB's own keyboard hook,
	// which emits a WM_KEYDOWN per hardware repeat).
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
