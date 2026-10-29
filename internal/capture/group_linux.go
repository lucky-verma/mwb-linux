//go:build linux

package capture

import (
	"log/slog"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/group"
)

// Group mode: this machine is one screen in a row of up to six. Whichever
// machine's keyboard and mouse are used drives the one shared cursor; the
// others follow the group's announcements of where it is (Arrive, Follow)
// and forward their own input there.

// switchToGroup hands the cursor to the machine past side of this screen,
// where (x, y) is the point on this screen's edge the cursor left from.
func (c *Capturer) switchToGroup(side string, x, y int32) {
	t, self, ok := c.nav.Beyond("", side)
	if !ok || self || t.Conn == nil {
		return // no machine online that way: the edge is a wall
	}
	now := time.Now()
	if now.Sub(c.lastSwitch) < 100*time.Millisecond {
		return
	}
	c.lastSwitch = now

	// The along-edge position: the height on a left/right edge, the horizontal
	// offset on an up/down edge.
	var along int32
	if side == "up" || side == "down" {
		along = clampWire(int32(int64(x) * 65535 / int64(max(1, c.screen.Width))))
	} else {
		along = clampWire(int32(int64(y) * 65535 / int64(max(1, c.screen.Height))))
	}
	slog.Info("screen edge hit, moving the cursor", "edge", side, "to", t.Name, "along", along)

	c.mu.Lock()
	c.active = false
	c.switchSent = now
	c.at, c.conn = t.Name, t.Conn
	c.remoteW, c.remoteH = t.Width, t.Height
	c.edgeSide = side // the direction of travel: land on the far side of it
	entryX, entryY := c.remoteEntryLocked(along)
	c.canReturn = false
	conn, send := c.conn, c.sendFn
	c.mu.Unlock()

	c.applyIsolation()
	go func() {
		// The announcement goes first on every connection, so the target
		// knows the cursor is coming before the burst arrives.
		c.nav.Moved(t.Name, group.Opposite(side), along)
		sendEntryBurst(conn, send, entryX, entryY)
	}()
}

// crossRemoteLocked moves the cursor on when the virtual cursor reaches a
// side of the remote screen it is on: back here, or to the next machine.
// It reports whether the cursor left that screen. c.mu must be held; it may
// be released and re-acquired.
func (c *Capturer) crossRemoteLocked() bool {
	// Every edge the virtual cursor is touching. At a corner more than one is,
	// so try each and take the first with a machine beyond it: a wall on one
	// side must not stop a crossing that another side allows.
	var sides []string
	if c.remoteX <= 0 {
		sides = append(sides, "left")
	}
	if c.remoteX >= c.remoteW-1 {
		sides = append(sides, "right")
	}
	if c.remoteY <= 0 {
		sides = append(sides, "up")
	}
	if c.remoteY >= c.remoteH-1 {
		sides = append(sides, "down")
	}
	var side string
	var t group.Target
	var self, ok bool
	for _, s := range sides {
		if tt, sf, o := c.nav.Beyond(c.at, s); o {
			side, t, self, ok = s, tt, sf, o
			break
		}
	}
	if !ok {
		return false // the end of the arrangement, or nobody online past it
	}
	// The along-edge position on the edge being crossed: the vertical position
	// on a left/right edge, the horizontal one on an up/down edge.
	var along int32
	if side == "up" || side == "down" {
		along = clampWire(int32(int64(c.remoteX) * 65535 / int64(max(1, c.remoteW))))
	} else {
		along = clampWire(int32(int64(c.remoteY) * 65535 / int64(max(1, c.remoteH))))
	}

	if self {
		slog.Info("cursor came back to this machine", "from", c.at, "side", group.Opposite(side))
		c.at = ""
		c.edgeSide = group.Opposite(side) // where it lands here
		// remoteX/remoteY already hold the along-edge position; SafeEntryPosition
		// maps them to the local landing.
		nav := c.nav
		c.returnLocalLocked(func() { nav.Moved("", group.Opposite(side), along) })
		return true
	}
	if t.Conn == nil {
		return false
	}
	slog.Info("cursor moving on", "from", c.at, "to", t.Name, "side", side)
	c.at, c.conn = t.Name, t.Conn
	c.remoteW, c.remoteH = t.Width, t.Height
	c.edgeSide = side
	entryX, entryY := c.remoteEntryLocked(along)
	c.canReturn = false
	c.switchSent = time.Now()
	conn, send, nav := c.conn, c.sendFn, c.nav
	c.mu.Unlock()
	nav.Moved(t.Name, group.Opposite(side), along)
	go sendEntryBurst(conn, send, entryX, entryY)
	c.mu.Lock()
	return true
}

// Arrive: another machine announced the cursor is now here, entering from
// side at height y (0-65535).
func (c *Capturer) Arrive(side string, y int32) {
	c.mu.Lock()
	if c.stopped || c.active {
		c.mu.Unlock()
		return
	}
	c.at = ""
	if side != "" {
		c.edgeSide = side
	}
	// Keep the along-edge position on the axis the entry side runs along, so the
	// landing (SafeEntryPosition) matches where the cursor crossed.
	if side == "up" || side == "down" {
		c.remoteX = int32(int64(y) * int64(c.remoteW) / 65535)
	} else {
		c.remoteY = int32(int64(y) * int64(c.remoteH) / 65535)
	}
	c.mu.Unlock()
	c.SetActive(true)
}

// Follow: another machine announced the cursor is now on t. This machine's
// keyboard and mouse go there until the cursor moves again.
func (c *Capturer) Follow(t group.Target, side string, y int32) {
	if t.Conn == nil {
		return
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.active = false
	c.switchSent = time.Now()
	c.at, c.conn = t.Name, t.Conn
	c.remoteW, c.remoteH = t.Width, t.Height
	c.edgeSide = group.Opposite(side) // the direction it travelled
	if side == "" {
		c.remoteX, c.remoteY = c.remoteW/2, c.remoteH/2 // taken back without crossing an edge
	} else {
		c.remoteEntryLocked(y) // y is the along-edge position; sets remoteX and remoteY
	}
	c.canReturn = false
	c.mu.Unlock()
	c.applyIsolation()
}

// Lost: the connection to the named machine ended. If this machine's input
// was going there, it stays here again.
func (c *Capturer) Lost(name string) {
	c.mu.Lock()
	here := c.active || c.at != name
	c.mu.Unlock()
	if here {
		return
	}
	slog.Info("the machine with the cursor went away; taking it back", "machine", name)
	c.mu.Lock()
	c.at = ""
	c.mu.Unlock()
	c.SetActive(true)
}

// RemoteAt reports which machine this machine's input goes to, "" when the
// cursor is here.
func (c *Capturer) RemoteAt() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		return ""
	}
	return c.at
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
