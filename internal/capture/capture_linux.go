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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/group"
	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const (
	evKey = 0x01
	evRel = 0x02

	relX      = 0x00
	relY      = 0x01
	relHWheel = 0x06
	relWheel  = 0x08

	// Mice report their back/forward buttons as BTN_SIDE/BTN_EXTRA, or as
	// BTN_BACK/BTN_FORWARD.
	btnForward = 0x115
	btnBack    = 0x116

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

	// Land 50 pixels inside the local screen and 8% inside the remote desktop.
	entryClearance      = 50
	remoteEntryFraction = 5243
	rearmDistance       = 20
	remoteReturnMargin  = 32
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
	pointer       pointerPositioner          // one persistent X11 connection for edge polling
	edge          edgeSource                 // Wayland: edge window that reports touches; nil polls pointer
	inward        int32                      // Wayland: raw inward motion since the edge was disarmed
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
	accel         pointerAccel              // speed-dependent gain on top of accelMult
	// Motion of the mouse report being read, sent as one move when the
	// report ends (SYN_REPORT) rather than one per axis.
	pendDX, pendDY int32
	out            *outbox // nil until Run: sends then happen inline (tests)
	canSwitch      bool    // true once cursor has been away from edge since activation
	canReturn      bool    // true once cursor has moved away from the remote return edge
	hotkeyCtrl     bool    // tracks Ctrl key state for hotkey detection
	hotkeyAlt      bool    // tracks Alt key state for hotkey detection
	// OnActivated runs whenever this capturer transitions from remote control
	// back to local ownership, including its own virtual-edge return path.
	OnActivated func()

	// linuxPeer enables the cursor-ownership packets an mwb-linux peer needs:
	// MachineSwitched when the cursor crosses to it and HideMouse when this
	// machine takes it back. PowerToys works this out itself, so they are only
	// sent for a Linux peer. Set before Run and not changed afterwards.
	linuxPeer bool
	sendFn    func(*protocol.Packet) error // test seam; nil uses conn.SendPacket

	// Group mode (nav != nil): this machine is one screen in a row. The
	// cursor can leave through either edge that has a neighbour and walk
	// across several remote screens; at names the screen it is on while
	// remote, and edgeSide is the direction it last travelled. Set before Run.
	nav        Navigator
	at         string
	watchLeft  bool
	watchRight bool
	watchUp    bool
	watchDown  bool
	edges      map[string]edgeSource // Wayland: one edge window per watched side
}

// Navigator places this machine in a group's row of screens; *group.Group
// implements it.
type Navigator interface {
	// Beyond returns the machine past side of the machine named from ("" =
	// this one); self reports that it is this machine.
	Beyond(from, side string) (t group.Target, self, ok bool)
	// Moved announces that this machine's input moved the cursor to the
	// machine named to ("" = this one), entering from side at the along-edge
	// position y.
	Moved(to, side string, y int32)
	// NeighborEdges reports every side of this screen that borders another
	// machine: "left", "right", "up", "down".
	NeighborEdges() map[string]bool
}

// SetNavigator puts the capturer in group mode. Call before Run.
func (c *Capturer) SetNavigator(n Navigator) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nav = n
	c.linuxPeer = false // group members learn where the cursor is from n
	if n != nil {
		e := n.NeighborEdges()
		c.watchLeft, c.watchRight = e["left"], e["right"]
		c.watchUp, c.watchDown = e["up"], e["down"]
	}
}

// trackedDevice is one open /dev/input/event* node.
type trackedDevice struct {
	file     *os.File
	grab     bool // keyboard or pointer: suppress it while the cursor is remote
	grabbed  bool // whether the exclusive grab is currently held
	touchpad *touchpad
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
	onActivated := c.OnActivated
	if shouldEnable {
		c.switchSent = time.Time{}
		c.lastActivated = time.Now()
		c.canSwitch = false // must move away from local edge before next outbound switch
		c.inward = 0
		c.canReturn = false // must move away from remote edge before next return switch
	}
	c.mu.Unlock()
	// applyIsolation acquires c.mu internally — must be called after unlock.
	// Calling it under the lock caused a deadlock that froze all goroutines.
	if shouldEnable {
		c.recenter()
		c.applyIsolation()
		if onActivated != nil {
			onActivated()
		}
	}
}

// IsActive returns true if cursor is on this machine.
func (c *Capturer) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// SafeEntryPosition returns a cursor position inside from the switch edge,
// safe to move to after MachineSwitched without immediately re-triggering the edge.
func (c *Capturer) SafeEntryPosition() (x, y int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The coordinate parallel to the shared edge follows where the cursor was
	// on the remote screen; the one across it sits an inset in from the edge.
	mapY := c.screen.Height / 2
	if c.remoteH > 0 {
		mapY = max(0, min(c.screen.Height-1, int32(int64(c.remoteY)*int64(c.screen.Height)/int64(c.remoteH))))
	}
	mapX := c.screen.Width / 2
	if c.remoteW > 0 {
		mapX = max(0, min(c.screen.Width-1, int32(int64(c.remoteX)*int64(c.screen.Width)/int64(c.remoteW))))
	}
	switch c.edgeSide {
	case "left":
		return entryInset(c.screen.Width), mapY
	case "right":
		return c.screen.Width - entryInset(c.screen.Width), mapY
	case "up":
		return mapX, entryInset(c.screen.Height)
	case "down":
		return mapX, c.screen.Height - entryInset(c.screen.Height)
	default:
		return c.screen.Width / 2, mapY
	}
}

// clampWire keeps an along-edge coordinate within MWB's 0-65535 space.
func clampWire(v int32) int32 { return max(0, min(65535, v)) }

// recenter runs before releasing grabbed input on every local return path.
func (c *Capturer) recenter() {
	if c.pointer == nil {
		return
	}
	x, y := c.SafeEntryPosition()
	if err := c.pointer.MoveTo(x, y); err != nil {
		slog.Warn("could not recenter cursor before releasing input", "err", err)
	}
}

// entryInset bounds the shared edge clearance for narrow screens.
func entryInset(width int32) int32 {
	return max(1, min(int32(entryClearance), (width-1)/2))
}

func remoteEntryInset(width int32) int32 {
	return max(1, int32((int64(width)*remoteEntryFraction+32767)/65535))
}

// remoteEntryLocked places the virtual cursor after a crossing in direction
// c.edgeSide, with alongWire (0-65535) its position along the edge it came
// through. It sets c.remoteX and c.remoteY in pixels and returns the entry
// position as wire coordinates (xWire, yWire) for the absolute burst.
func (c *Capturer) remoteEntryLocked(alongWire int32) (xWire, yWire int32) {
	switch c.edgeSide {
	case "up", "down":
		// Crossing a top/bottom edge: land an inset in across it, the along
		// coordinate runs horizontally.
		inset := remoteEntryInset(c.remoteH)
		py := inset // moved down: land near the top
		if c.edgeSide == "up" {
			py = c.remoteH - inset
		}
		c.remoteY = py
		c.remoteX = int32(int64(alongWire) * int64(c.remoteW) / 65535)
		return int32(int64(c.remoteX) * 65535 / int64(max(1, c.remoteW))), int32(int64(py) * 65535 / int64(max(1, c.remoteH)))
	default: // "left", "right"
		inset := remoteEntryInset(c.remoteW)
		px := inset // moved right: land near the left
		if c.edgeSide == "left" {
			px = c.remoteW - inset
		}
		c.remoteX = px
		c.remoteY = int32(int64(alongWire) * int64(c.remoteH) / 65535)
		return int32(int64(px) * 65535 / int64(max(1, c.remoteW))), alongWire
	}
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

	// A smaller landing must not fall inside the accepted return margin.
	margin := min(int32(remoteReturnMargin), remoteEntryInset(remoteW)/2)
	switch c.edgeSide {
	case "left":
		return c.remoteX >= remoteW-margin
	case "right":
		return c.remoteX <= margin
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

// SetLinuxPeer marks the peer as another mwb-linux instance. Call before Run.
func (c *Capturer) SetLinuxPeer(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.linuxPeer = on
}

// notifyPeer sends a cursor-ownership packet to a Linux peer. It is a no-op for
// a PowerToys peer, so that path sends exactly what it always has. The caller
// must not hold c.mu.
func (c *Capturer) notifyPeer(t protocol.PackageType) {
	c.mu.Lock()
	linux, conn, send := c.linuxPeer, c.conn, c.sendFn
	c.mu.Unlock()
	if !linux {
		return
	}
	pkt := &protocol.Packet{Type: t}
	if send == nil {
		if conn == nil {
			return
		}
		pkt.Src, pkt.Des = conn.MachineID, conn.RemoteID
		send = conn.SendPacket
	}
	if err := send(pkt); err != nil {
		slog.Debug("send cursor ownership packet failed", "type", t, "err", err)
	}
}

// reclaimLocal returns the cursor to this machine because of something done
// here (hotkey, absolute pointer), and tells a Linux peer it no longer has it.
func (c *Capturer) reclaimLocal() {
	wasActive := c.IsActive()
	c.SetActive(true)
	if !wasActive {
		c.notifyPeer(protocol.HideMouse)
		c.mu.Lock()
		nav := c.nav
		c.mu.Unlock()
		if nav != nil {
			nav.Moved("", "", 32767)
		}
	}
}

// YieldToPeer handles a Linux peer's HideMouse: the peer took the cursor back
// while it was on this machine. Local input is isolated and forwarded, as it is
// after crossing the edge locally, so either machine's keyboard and mouse
// follow the single shared cursor. The virtual remote cursor starts at the
// same landing point a local crossing uses.
func (c *Capturer) YieldToPeer() {
	c.mu.Lock()
	if !c.active || c.stopped {
		c.mu.Unlock()
		return
	}
	pointer := c.pointer
	c.mu.Unlock()

	// Query outside the lock: it is a round trip to the X server.
	y := int32(-1)
	if pointer != nil {
		if _, py, err := pointer.Position(); err == nil {
			y = py
		}
	}

	c.mu.Lock()
	if !c.active || c.stopped {
		c.mu.Unlock()
		return
	}
	if y < 0 || y >= c.screen.Height {
		y = c.screen.Height / 2
	}
	slog.Info("peer took the cursor back; forwarding local input to it")
	c.active = false
	c.switchSent = time.Now()
	c.remoteEntryLocked(clampWire(int32(int64(y) * 65535 / int64(max(1, c.screen.Height)))))
	c.canReturn = false
	c.mu.Unlock()

	// applyIsolation acquires c.mu itself.
	c.applyIsolation()
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
	if c.pointer != nil {
		c.pointer.Close()
	}
	if c.edge != nil {
		c.edge.Close()
	}
	for _, e := range c.edges {
		e.Close()
	}
	c.wg.Wait()
}

// Run starts edge detection polling and evdev monitoring.
// Validates all preconditions before starting any goroutines.
func (c *Capturer) Run() error {
	discover := c.findDevicesFn
	if discover == nil {
		discover = findInputDevices
	}
	devices, err := discover()
	if err != nil {
		return fmt.Errorf("find input devices: %w", err)
	}
	if c.pointer == nil {
		c.pointer, err = newX11Pointer(getDisplay())
		if err != nil {
			return fmt.Errorf("connect to X11 for cursor position: %w", err)
		}
	}
	if c.nav != nil && c.edges == nil && isWaylandSession() {
		c.edges = map[string]edgeSource{}
		for side, watch := range map[string]bool{"left": c.watchLeft, "right": c.watchRight, "up": c.watchUp, "down": c.watchDown} {
			if !watch {
				continue
			}
			strip, err := newEdgeStrip(getDisplay(), side)
			if err != nil {
				slog.Warn("Wayland session: could not create the edge window; the cursor may not cross there", "edge", side, "err", err)
				continue
			}
			c.edges[side] = strip
		}
		// Arm only after the mouse moves inward, as below.
		c.mu.Lock()
		c.canSwitch = false
		c.inward = 0
		c.mu.Unlock()
		slog.Info("Wayland session: detecting the screen edges with XWayland edge windows",
			"left", c.watchLeft, "right", c.watchRight, "up", c.watchUp, "down", c.watchDown)
	} else if c.nav == nil && c.edge == nil && isWaylandSession() {
		strip, err := newEdgeStrip(getDisplay(), c.edgeSide)
		if err != nil {
			slog.Warn("Wayland session: could not create the edge window; the cursor may not cross to the remote", "err", err)
		} else {
			// The strip reports a touch as soon as it maps if the cursor is
			// already on the edge, as it is after a reconnect while the
			// cursor was remote. Arm only after the mouse moves inward.
			c.mu.Lock()
			c.canSwitch = false
			c.inward = 0
			c.mu.Unlock()
			c.edge = strip
			slog.Info("Wayland session: detecting the screen edge with an XWayland edge window", "edge", c.edgeSide)
		}
	}
	slog.Info("found input devices", "count", len(devices))

	out := newOutbox()
	c.mu.Lock()
	c.out = out
	c.mu.Unlock()
	c.wg.Add(3)
	go func() {
		defer c.wg.Done()
		out.run(c.stopCh)
	}()
	go func() {
		defer c.wg.Done()
		c.pollCursorEdge()
	}()
	go func() {
		defer c.wg.Done()
		c.watchDevices()
	}()
	for _, d := range devices {
		c.addDevice(d)
	}
	c.mu.Lock()
	targets, touchpads := 0, 0
	for _, d := range c.devices {
		if d.grab {
			targets++
		}
		if d.touchpad != nil {
			touchpads++
		}
	}
	c.mu.Unlock()
	slog.Info("classified input devices", "tracked", len(devices), "keyboards_and_pointers", targets, "touchpads", touchpads)
	return nil
}

// addDevice opens, classifies and starts monitoring one device node. Devices
// that cannot be opened are skipped: /dev/input entries appear before udev has
// applied permissions, and a node can vanish between listing and opening.
//
// It reports whether it added a keyboard, mouse or touchpad.
func (c *Capturer) addDevice(path string) bool {
	c.mu.Lock()
	_, known := c.devices[path]
	stopped := c.stopped
	c.mu.Unlock()
	if known || stopped {
		return false
	}

	// O_NONBLOCK is what makes Stop() able to finish. Opened blocking, the fd is
	// not registered with Go's poller, a parked read(2) is not interrupted by
	// Close(), and monitorDevice only returns once that device happens to emit
	// an event — which silent nodes (power buttons, audio-jack detect) never do.
	// Stop() then waits forever in wg.Wait(). Non-blocking fds go through the
	// poller, where Close() evicts pending reads, with no added input latency.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	grabbable := isGrabTarget(f)
	var tp *touchpad
	if grabbable {
		tp = touchpadFor(f)
	}
	if tp != nil {
		c.mu.Lock()
		tp.scale = c.accelMult / defaultAccelMultiplier
		c.mu.Unlock()
	}

	c.mu.Lock()
	// Re-check under the lock: Stop() may have run, and a concurrent refresh
	// may have added this same path.
	if c.stopped {
		c.mu.Unlock()
		_ = f.Close()
		return false
	}
	if _, dup := c.devices[path]; dup {
		c.mu.Unlock()
		_ = f.Close()
		return false
	}
	if c.devices == nil {
		c.devices = make(map[string]*trackedDevice)
	}
	c.devices[path] = &trackedDevice{file: f, grab: grabbable, touchpad: tp}
	// wg.Add must happen under the same lock that guards c.stopped, otherwise
	// it can race an in-progress wg.Wait() in Stop().
	c.wg.Add(1)
	c.mu.Unlock()

	go func() {
		defer c.wg.Done()
		c.monitorDevice(f)
	}()
	return grabbable
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
		if c.addDevice(p) {
			slog.Info("input device connected", "device", p)
		}
	}
}

// deviceRescan is how often /dev/input is checked for keyboards and mice
// that connected since the last look.
const deviceRescan = 2 * time.Second

// watchDevices picks up devices that appear while mwb runs, such as a
// Bluetooth mouse reconnecting after the screen sleeps, and grabs them at
// once if the cursor is on another machine. Without it, a device that
// appeared while the cursor was here would be read only from the next
// switch, and on Wayland, where re-arming an edge relies on reading the
// mouse, that mouse alone could not cross.
func (c *Capturer) watchDevices() {
	t := time.NewTicker(deviceRescan)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
			if c.IsActive() {
				c.grabMu.Lock()
				c.refreshDevices()
				c.grabMu.Unlock()
			} else {
				c.applyIsolation() // rescans, and grabs what is new
			}
		}
	}
}

// pollCursorEdge checks the actual cursor position and triggers switches.
// On Wayland the polled position is frozen, so edge touches come from the
// edge window instead and re-arming comes from evdev motion (trackInward).
func (c *Capturer) pollCursorEdge() {
	slog.Info("edge polling started", "edge", c.edgeSide, "screenWidth", c.screen.Width,
		"group", c.nav != nil, "left", c.watchLeft, "right", c.watchRight)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	// Edge windows report touches on Wayland: one for the pair's edge, or
	// one per watched side in a group.
	var hitsLeft, hitsRight, hitsUp, hitsDown <-chan int32
	if c.edge != nil {
		if c.edgeSide == "right" {
			hitsRight = c.edge.Hits()
		} else {
			hitsLeft = c.edge.Hits()
		}
	}
	if e := c.edges["left"]; e != nil {
		hitsLeft = e.Hits()
	}
	if e := c.edges["right"]; e != nil {
		hitsRight = e.Hits()
	}
	if e := c.edges["up"]; e != nil {
		hitsUp = e.Hits()
	}
	if e := c.edges["down"]; e != nil {
		hitsDown = e.Hits()
	}
	windows := c.edge != nil || len(c.edges) > 0

	// hit fires when an edge window reports a touch. along is the coordinate
	// the window reports along itself: Y for a left/right edge, X for up/down.
	hit := func(side string, along int32) {
		c.mu.Lock()
		armed := c.active && c.canSwitch
		c.mu.Unlock()
		if !armed {
			return
		}
		var x, y int32
		switch side {
		case "left":
			x, y = 0, along
		case "right":
			x, y = c.screen.Width-1, along
		case "up":
			x, y = along, 0
		case "down":
			x, y = along, c.screen.Height-1
		}
		c.crossLocalEdge(side, x, y)
	}

	ticks := 0
	errCount := 0
	for {
		select {
		case <-c.stopCh:
			return
		case y, ok := <-hitsLeft:
			if !ok {
				hitsLeft = nil
				continue
			}
			hit("left", y)
		case y, ok := <-hitsRight:
			if !ok {
				hitsRight = nil
				continue
			}
			hit("right", y)
		case x, ok := <-hitsUp:
			if !ok {
				hitsUp = nil
				continue
			}
			hit("up", x)
		case x, ok := <-hitsDown:
			if !ok {
				hitsDown = nil
				continue
			}
			hit("down", x)
		case <-ticker.C:
			if windows {
				// Re-raise about once a second so newly mapped windows
				// cannot cover the strips.
				if ticks++; ticks%100 == 0 {
					if c.edge != nil {
						c.edge.Raise()
					}
					for _, e := range c.edges {
						e.Raise()
					}
				}
				continue
			}
			if !c.IsActive() {
				continue
			}
			x, y, err := c.pointer.Position()
			if err != nil {
				errCount++
				if errCount <= 3 {
					slog.Warn("cursor position query failed", "err", err, "count", errCount)
				}
				continue
			}
			if errCount > 0 {
				errCount = 0
			}
			if side := c.polledEdge(x, y); side != "" {
				c.crossLocalEdge(side, x, y)
			}
		}
	}
}

// polledEdge tracks whether the cursor has been away from the edge since
// activation (so it cannot loop straight back), and returns the side it is
// touching once armed, or "".
func (c *Capturer) polledEdge(x, y int32) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	edgeZone := int32(rearmDistance) // pixels from edge — must move this far inward to re-arm
	left, right := c.edgeSide == "left", c.edgeSide == "right"
	up, down := false, false
	if c.nav != nil {
		left, right, up, down = c.watchLeft, c.watchRight, c.watchUp, c.watchDown
		// Re-arm once the cursor sits clear of every watched edge.
		awayX := x > edgeZone && x < c.screen.Width-edgeZone
		awayY := y > edgeZone && y < c.screen.Height-edgeZone
		if awayX && awayY {
			c.canSwitch = true
		}
	} else {
		if left && x > edgeZone {
			c.canSwitch = true
		}
		if right && x < c.screen.Width-edgeZone {
			c.canSwitch = true
		}
	}
	if !c.canSwitch {
		return ""
	}
	if left && x <= 1 {
		return "left"
	}
	if right && x >= c.screen.Width-1 {
		return "right"
	}
	if up && y <= 1 {
		return "up"
	}
	if down && y >= c.screen.Height-1 {
		return "down"
	}
	return ""
}

// crossLocalEdge hands the cursor on after it touched side of this screen.
func (c *Capturer) crossLocalEdge(side string, x, y int32) {
	if c.nav != nil {
		c.switchToGroup(side, x, y)
		return
	}
	c.switchToRemote(x, y)
}

// trackInward re-arms the edge on Wayland once the physical pointer has moved
// rearmDistance raw units away from the edge since the last disarm.
func (c *Capturer) trackInward(dx, dy int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if (c.edge == nil && len(c.edges) == 0) || c.canSwitch {
		return // edge windows are only used on Wayland; X11 polling re-arms itself
	}
	// Count motion away from the edge the cursor last entered through: inward is
	// along X for a left/right edge, along Y for an up/down one.
	var inward int32
	switch c.edgeSide {
	case "right":
		inward = -dx
	case "left":
		inward = dx
	case "down":
		inward = -dy
	case "up":
		inward = dy
	default:
		inward = abs32(dx) + abs32(dy) // no known edge yet: any motion moves away
	}
	c.inward = max(0, c.inward+inward)
	if c.inward >= rearmDistance {
		c.canSwitch = true
		c.inward = 0
	}
}

// switchToRemote hands the cursor to the remote after it hit the local edge at (x, y).
func (c *Capturer) switchToRemote(x, y int32) {
	now := time.Now()
	if now.Sub(c.lastSwitch) < 100*time.Millisecond {
		return
	}
	c.lastSwitch = now

	slog.Info("screen edge hit, switching to remote", "edge", c.edgeSide, "x", x, "y", y)

	c.mu.Lock()
	c.active = false
	c.switchSent = time.Now()
	// A pair always shares a left/right edge; the along-edge coordinate is the
	// height. remoteEntryLocked sets the virtual cursor and returns the burst
	// landing.
	entryX, entryY := c.remoteEntryLocked(clampWire(int32(int64(y) * 65535 / int64(max(1, c.screen.Height)))))
	c.canReturn = false // must move away from return edge first
	c.mu.Unlock()

	// Suppress local input (synchronous — one ioctl per device)
	c.applyIsolation()

	// Send mouse burst to the entry position on remote
	// Multiple packets help Windows MWB register the switch reliably
	c.mu.Lock()
	conn, send := c.conn, c.sendFn
	c.mu.Unlock()
	go func() {
		// A Linux peer cannot tell injected motion from its own,
		// so tell it the cursor now lives there. Sent ahead of the
		// burst so the peer's recenter cannot land after it.
		c.notifyPeer(protocol.MachineSwitched)
		sendEntryBurst(conn, send, entryX, entryY)
	}()
}

// sendEntryBurst moves the remote cursor to its landing point. Multiple
// packets help Windows MWB register the switch reliably.
func sendEntryBurst(conn *network.Conn, send func(*protocol.Packet) error, entryX, entryY int32) {
	if send == nil {
		if conn == nil {
			return
		}
		send = conn.SendPacket
	}
	for i := 0; i < 5; i++ {
		mouse := &protocol.Packet{Type: protocol.Mouse}
		if conn != nil {
			mouse.Src, mouse.Des = conn.MachineID, conn.RemoteID
		}
		mouse.Mouse.X = entryX
		mouse.Mouse.Y = entryY
		mouse.Mouse.DwFlags = protocol.WM_MOUSEMOVE
		_ = send(mouse)
		time.Sleep(5 * time.Millisecond)
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
	defer func() {
		// The device is gone (or mwb is stopping). Forget it, so that when
		// it comes back, often under the same /dev/input path, the rescan
		// opens it again instead of keeping this closed file.
		c.mu.Lock()
		if d := c.devices[f.Name()]; d != nil && d.file == f {
			delete(c.devices, f.Name())
		}
		c.mu.Unlock()
		_ = f.Close()
	}()
	slog.Debug("monitoring device", "path", f.Name())
	buf := make([]byte, inputEventSize*32)
	c.mu.Lock()
	var tp *touchpad
	if d := c.devices[f.Name()]; d != nil {
		tp = d.touchpad
	}
	c.mu.Unlock()
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
			if tp != nil {
				tp.handle(ev, touchpadSink{c})
				continue
			}
			if ev.Type == uint16(evAbsType) {
				c.mu.Lock()
				d := c.devices[f.Name()]
				recoverAbsolute := d != nil && d.grab && d.grabbed
				c.mu.Unlock()
				if !recoverAbsolute {
					continue
				}
			}
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
		if ev.Type == evRel && ev.Code == relX {
			c.trackInward(ev.Value, 0)
		} else if ev.Type == evRel && ev.Code == relY {
			c.trackInward(0, ev.Value)
		}
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
	case evSyn:
		if ev.Code == synReport {
			c.mu.Lock()
			c.flushMotionLocked()
			c.mu.Unlock()
		}
	case uint16(evAbsType):
		// Raw absolute movement cannot be forwarded. Return local control
		// explicitly without altering the normal mouse-switching paths.
		slog.Warn("absolute pointer forwarding is unsupported; restoring local input")
		c.reclaimLocal()
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

	t := ev.Sec*1_000_000 + ev.Usec
	switch ev.Code {
	case relX:
		gain := c.accelMult * c.accel.factorFor(t, ev.Value, 0)
		c.pendDX += applyAcceleration(ev.Value, gain)
	case relY:
		gain := c.accelMult * c.accel.factorFor(t, 0, ev.Value)
		c.pendDY += applyAcceleration(ev.Value, gain)
	case relWheel:
		c.flushMotionLocked()
		c.sendMouseLocked(0, 0, ev.Value*120, protocol.WM_MOUSEWHEEL)
	case relHWheel:
		// Both positive to the right.
		c.flushMotionLocked()
		c.sendMouseLocked(0, 0, ev.Value*120, protocol.WM_MOUSEHWHEEL)
	}
}

// flushMotionLocked moves the remote cursor by the motion gathered from the
// current mouse report, as one move. c.mu must be held.
func (c *Capturer) flushMotionLocked() {
	dx, dy := c.pendDX, c.pendDY
	if dx == 0 && dy == 0 {
		return
	}
	c.pendDX, c.pendDY = 0, 0
	if c.active {
		return // the cursor came back meanwhile
	}
	c.moveRemoteLocked(dx, dy)
}

// returnLocalLocked brings the cursor back to this machine after the virtual
// cursor left the remote screen towards it, then runs announce. c.mu must be
// held; it is released and re-acquired.
func (c *Capturer) returnLocalLocked(announce func()) {
	c.active = true
	c.pendDX, c.pendDY = 0, 0
	onActivated := c.OnActivated
	c.switchSent = time.Time{}
	c.lastActivated = time.Now()
	c.canSwitch = false // block re-trigger until cursor moves away from edge
	c.inward = 0
	c.mu.Unlock()

	// Move cursor away from edge SYNCHRONOUSLY before releasing the grab.
	// Ordering is load-bearing: release first and in-flight physical mouse
	// motion drives the cursor straight back into the edge, bouncing the
	// switch. Move first, then hand input back.
	c.recenter()

	c.applyIsolation()
	if onActivated != nil {
		onActivated()
	}
	announce()
	c.mu.Lock()
}

// moveRemoteLocked moves the virtual remote cursor by (dx, dy) pixels, returns
// control when it reaches the return edge, and otherwise forwards the new
// position. c.mu must be held; it is released and re-acquired on return.
func (c *Capturer) moveRemoteLocked(dx, dy int32) {
	c.remoteX = max(0, min(c.remoteW, c.remoteX+dx))
	c.remoteY = max(0, min(c.remoteH, c.remoteY+dy))

	// Like the local edge gate, require deliberate inward movement before return.
	// This prevents jitter/momentum from the initial switch from bouncing back.
	// The gate is on the axis the cursor crossed: horizontal for a left/right
	// entry, vertical for an up/down one.
	switch c.edgeSide {
	case "left":
		if c.remoteX < c.remoteW-remoteEntryInset(c.remoteW) {
			c.canReturn = true
		}
	case "right":
		if c.remoteX > remoteEntryInset(c.remoteW) {
			c.canReturn = true
		}
	case "up":
		if c.remoteY < c.remoteH-remoteEntryInset(c.remoteH) {
			c.canReturn = true
		}
	case "down":
		if c.remoteY > remoteEntryInset(c.remoteH) {
			c.canReturn = true
		}
	}

	if c.nav != nil {
		if c.canReturn && c.crossRemoteLocked() {
			return
		}
		c.sendPositionLocked()
		return
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
		slog.Info("remote edge hit — switching back to Ubuntu", "remoteX", c.remoteX, "remoteY", c.remoteY)
		c.returnLocalLocked(func() { c.notifyPeer(protocol.HideMouse) })
		return
	}

	c.sendPositionLocked()
}

// sendPositionLocked sends the virtual cursor's absolute position.
func (c *Capturer) sendPositionLocked() {
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
			c.reclaimLocal()
			return
		}
	}

	// Mouse buttons
	if btn, ok := mouseButtons[ev.Code]; ok {
		if !c.IsActive() && (ev.Value == 0 || ev.Value == 1) {
			flags := btn.up
			if ev.Value == 1 {
				flags = btn.down
			}
			// Use current virtual cursor position so clicks register at the
			// correct location on Windows, not always at top-left (0,0).
			c.mu.Lock()
			c.flushMotionLocked()
			absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
			absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
			c.mu.Unlock()
			c.sendMouse(absX, absY, btn.data, flags)
		}
		return
	}

	// Keyboard
	if c.IsActive() {
		return
	}

	vk, extended, ok := input.KeyCodeToVKExtended(ev.Code)
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
	if extended {
		dwFlags |= protocol.LLKHF_EXTENDED
	}

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	pkt := &protocol.Packet{
		Type: protocol.Keyboard,
		Src:  conn.MachineID,
		Des:  conn.RemoteID,
	}
	pkt.Keyboard.WVk = vk
	pkt.Keyboard.DwFlags = dwFlags
	c.post(pkt, conn, nil, false)
}

// post sends pkt to conn (or through send), via the outbox once Run has
// started it. move marks a pointer move, which a newer one may replace.
func (c *Capturer) post(pkt *protocol.Packet, conn *network.Conn, send func(*protocol.Packet) error, move bool) {
	c.mu.Lock()
	out := c.out
	c.mu.Unlock()
	if out != nil {
		out.put(pkt, conn, send, move)
		return
	}
	if send == nil {
		if conn == nil {
			return
		}
		send = conn.SendPacket
	}
	if err := send(pkt); err != nil {
		slog.Debug("send input failed", "type", pkt.Type, "err", err)
	}
}

// mouseButton is how one evdev button travels: its down and up messages, and
// the mouse data that goes with them (which X button, as MWB sends it).
type mouseButton struct{ down, up, data int32 }

var mouseButtons = map[uint16]mouseButton{
	input.BTN_LEFT:   {protocol.WM_LBUTTONDOWN, protocol.WM_LBUTTONUP, 0},
	input.BTN_RIGHT:  {protocol.WM_RBUTTONDOWN, protocol.WM_RBUTTONUP, 0},
	input.BTN_MIDDLE: {protocol.WM_MBUTTONDOWN, protocol.WM_MBUTTONUP, 0},
	input.BTN_SIDE:   {protocol.WM_XBUTTONDOWN, protocol.WM_XBUTTONUP, 1},
	btnBack:          {protocol.WM_XBUTTONDOWN, protocol.WM_XBUTTONUP, 1},
	input.BTN_EXTRA:  {protocol.WM_XBUTTONDOWN, protocol.WM_XBUTTONUP, 2},
	btnForward:       {protocol.WM_XBUTTONDOWN, protocol.WM_XBUTTONUP, 2},
}

func (c *Capturer) sendMouse(x, y, wheelDelta, flags int32) {
	c.mu.Lock()
	conn, send, out := c.conn, c.sendFn, c.out
	c.mu.Unlock()
	queueMouse(out, conn, send, x, y, wheelDelta, flags)
}

// sendMouseLocked sends a mouse packet (caller must hold c.mu).
func (c *Capturer) sendMouseLocked(x, y, wheelDelta, flags int32) {
	queueMouse(c.out, c.conn, c.sendFn, x, y, wheelDelta, flags)
}

// queueMouse sends one mouse packet through out, or inline when out is nil.
func queueMouse(out *outbox, conn *network.Conn, send func(*protocol.Packet) error, x, y, wheelDelta, flags int32) {
	if out == nil {
		deliverMouse(conn, send, x, y, wheelDelta, flags)
		return
	}
	pkt := &protocol.Packet{Type: protocol.Mouse}
	if conn != nil {
		pkt.Src, pkt.Des = conn.MachineID, conn.RemoteID
	}
	pkt.Mouse.X, pkt.Mouse.Y = x, y
	pkt.Mouse.WheelDelta, pkt.Mouse.DwFlags = wheelDelta, flags
	out.put(pkt, conn, send, flags == protocol.WM_MOUSEMOVE)
}

// deliverMouse sends one mouse packet through send, or conn when send is nil.
func deliverMouse(conn *network.Conn, send func(*protocol.Packet) error, x, y, wheelDelta, flags int32) {
	pkt := &protocol.Packet{Type: protocol.Mouse}
	if conn != nil {
		pkt.Src, pkt.Des = conn.MachineID, conn.RemoteID
	}
	pkt.Mouse.X = x
	pkt.Mouse.Y = y
	pkt.Mouse.WheelDelta = wheelDelta
	pkt.Mouse.DwFlags = flags

	if send == nil {
		if conn == nil {
			return
		}
		send = conn.SendPacket
	}
	if err := send(pkt); err != nil {
		slog.Debug("send mouse failed", "err", err)
	}
}
