//go:build linux

package capture

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/group"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// fakeRow is a Navigator over a fixed arrangement where every machine is
// online. A row is the common case; a grid drives the two-dimensional tests.
type fakeRow struct {
	me    string
	grid  map[string]group.Pos
	conns map[string]*network.Conn

	mu    sync.Mutex
	moved []string
}

func newFakeRow(me string, order ...string) *fakeRow {
	return newFakeGrid(me, group.RowGrid(order))
}

func newFakeGrid(me string, grid map[string]group.Pos) *fakeRow {
	r := &fakeRow{me: me, grid: grid, conns: map[string]*network.Conn{}}
	for n := range grid {
		if n != me {
			r.conns[n] = &network.Conn{RemoteName: n}
		}
	}
	return r
}

func (r *fakeRow) Beyond(from, side string) (group.Target, bool, bool) {
	if from == "" {
		from = r.me
	}
	name, ok := group.BeyondGrid(r.grid, func(string) bool { return true }, from, side)
	if !ok {
		return group.Target{}, false, false
	}
	if name == r.me {
		return group.Target{Name: name}, true, true
	}
	return group.Target{Name: name, Conn: r.conns[name], Width: 1000, Height: 500}, false, true
}

func (r *fakeRow) Moved(to, side string, y int32) {
	if to == "" {
		to = r.me
	}
	r.mu.Lock()
	r.moved = append(r.moved, to+" "+side)
	r.mu.Unlock()
}

func (r *fakeRow) NeighborEdges() map[string]bool { return group.Edges(r.grid, r.me) }

func (r *fakeRow) lastMove() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.moved) == 0 {
		return ""
	}
	return r.moved[len(r.moved)-1]
}

func newGroupCapturer(t *testing.T, row *fakeRow) (*Capturer, func() int) {
	t.Helper()
	c, grabbed := newFakeIsolated(t, 1)
	c.screen = ScreenInfo{Width: 1920, Height: 1080}
	c.active, c.canSwitch = true, true
	c.accelMult = 1
	c.pointer = &recordingPointer{}
	c.sendFn = func(*protocol.Packet) error { return nil }
	c.SetNavigator(row)
	return c, grabbed
}

func waitMove(t *testing.T, row *fakeRow, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for row.lastMove() != want && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := row.lastMove(); got != want {
		t.Fatalf("last announcement %q, want %q", got, want)
	}
}

// moveBy pushes the virtual remote cursor, as mouse motion would.
func moveBy(c *Capturer, dx int32) { moveByXY(c, dx, 0) }

// moveByXY pushes the virtual remote cursor in two dimensions.
func moveByXY(c *Capturer, dx, dy int32) {
	c.mu.Lock()
	c.switchSent = time.Time{} // past the switch grace period
	c.moveRemoteLocked(dx, dy)
	c.mu.Unlock()
}

func TestGroupCursorWalksTheRowAndComesBack(t *testing.T) {
	row := newFakeRow("me", "me", "b", "c")
	c, grabbed := newGroupCapturer(t, row)
	if c.watchLeft || !c.watchRight {
		t.Fatal("the leftmost screen watches only its right edge")
	}
	if side := c.polledEdge(0, 540); side != "" {
		t.Fatalf("the left edge has no neighbour but reported %q", side)
	}

	c.crossLocalEdge("right", 1919, 540)
	waitMove(t, row, "b left")
	if c.IsActive() || c.RemoteAt() != "b" || grabbed() != 1 {
		t.Fatalf("not on b: active=%v at=%q grabbed=%d", c.IsActive(), c.RemoteAt(), grabbed())
	}

	// Walk off b's right edge onto c.
	moveBy(c, 200) // away from the entry edge first
	moveBy(c, 2000)
	waitMove(t, row, "c left")
	if c.RemoteAt() != "c" || c.conn != row.conns["c"] {
		t.Fatalf("not on c: at=%q", c.RemoteAt())
	}
	// c is the end of the row: its right edge is a wall.
	moveBy(c, 200)
	moveBy(c, 2000)
	if c.RemoteAt() != "c" {
		t.Fatal("walked past the end of the row")
	}
	// All the way back left: c, b, then here.
	moveBy(c, -5000)
	waitMove(t, row, "b right")
	moveBy(c, -200)
	moveBy(c, -5000)
	waitMove(t, row, "me right")
	if !c.IsActive() || grabbed() != 0 {
		t.Fatalf("did not come back: active=%v grabbed=%d", c.IsActive(), grabbed())
	}
	if p := c.pointer.(*recordingPointer); !p.moved || p.x < 1800 {
		t.Fatalf("landed at x=%d, want near the right edge it came in from", p.x)
	}
}

func TestGroupCursorWalksUpAndDown(t *testing.T) {
	// me on top, b directly below it (a one-column, two-row arrangement).
	row := newFakeGrid("me", map[string]group.Pos{"me": {Col: 0, Row: 0}, "b": {Col: 0, Row: 1}})
	c, grabbed := newGroupCapturer(t, row)
	if c.watchLeft || c.watchRight || c.watchUp || !c.watchDown {
		t.Fatalf("the top screen watches only its bottom edge: L%v R%v U%v D%v",
			c.watchLeft, c.watchRight, c.watchUp, c.watchDown)
	}

	// Cross the bottom edge onto b, which the cursor enters from the top.
	c.crossLocalEdge("down", 960, 1079)
	waitMove(t, row, "b up")
	if c.IsActive() || c.RemoteAt() != "b" || grabbed() != 1 {
		t.Fatalf("not on b: active=%v at=%q grabbed=%d", c.IsActive(), c.RemoteAt(), grabbed())
	}

	// b's bottom edge is a wall (nothing below): walking down stays on b.
	moveByXY(c, 0, 100) // away from the top entry edge first
	moveByXY(c, 0, 2000)
	if c.RemoteAt() != "b" {
		t.Fatal("walked past the bottom of the column")
	}

	// Walk back up off b's top edge: the cursor returns here.
	moveByXY(c, 0, -5000)
	waitMove(t, row, "me down")
	if !c.IsActive() || grabbed() != 0 {
		t.Fatalf("did not come back up: active=%v grabbed=%d", c.IsActive(), grabbed())
	}
	if p := c.pointer.(*recordingPointer); !p.moved || p.y < c.screen.Height-200 {
		t.Fatalf("landed at y=%d, want near the bottom edge it came in from (b is below me)", p.y)
	}
}

func TestPolledEdgeDetectsTopAndBottom(t *testing.T) {
	// me on top with b below: only the bottom edge has a neighbour.
	row := newFakeGrid("me", map[string]group.Pos{"me": {Col: 0, Row: 0}, "b": {Col: 0, Row: 1}})
	c, _ := newGroupCapturer(t, row) // 1920x1080, canSwitch=true
	if side := c.polledEdge(960, c.screen.Height-1); side != "down" {
		t.Fatalf("bottom edge with a neighbour below reported %q, want down", side)
	}
	if side := c.polledEdge(960, 0); side != "" {
		t.Fatalf("top edge has no neighbour but reported %q", side)
	}
	if side := c.polledEdge(0, 540); side != "" {
		t.Fatalf("left edge has no neighbour but reported %q", side)
	}
}

func TestGroupFollowArriveAndLost(t *testing.T) {
	row := newFakeRow("me", "a", "me", "c")
	c, grabbed := newGroupCapturer(t, row)
	if !c.watchLeft || !c.watchRight {
		t.Fatal("a middle screen watches both edges")
	}

	// Another machine moved the cursor onto c: this keyboard follows.
	c.Follow(group.Target{Name: "c", Conn: row.conns["c"], Width: 1000, Height: 500}, "left", 32767)
	if c.IsActive() || c.RemoteAt() != "c" || grabbed() != 1 {
		t.Fatalf("not following: active=%v at=%q", c.IsActive(), c.RemoteAt())
	}
	// A different machine went away: nothing changes.
	c.Lost("a")
	if c.IsActive() {
		t.Fatal("came back when an unrelated machine left")
	}
	// The machine with the cursor went away: take it back.
	c.Lost("c")
	if !c.IsActive() || grabbed() != 0 {
		t.Fatal("did not take the cursor back from a machine that left")
	}

	c.Follow(group.Target{Name: "a", Conn: row.conns["a"], Width: 1000, Height: 500}, "right", 0)
	c.Arrive("left", 100)
	if !c.IsActive() || c.RemoteAt() != "" {
		t.Fatal("Arrive did not bring the cursor here")
	}
	if !slices.Equal(row.moved, nil) {
		t.Fatalf("following must not announce anything, announced %v", row.moved)
	}
}
