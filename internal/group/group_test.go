package group

import (
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/network"
)

type nopInput struct{}

func (nopInput) MoveTo(x, y int32) error   { return nil }
func (nopInput) ButtonDown(uint16) error   { return nil }
func (nopInput) ButtonUp(uint16) error     { return nil }
func (nopInput) Wheel(int32) error         { return nil }
func (nopInput) HWheel(int32) error        { return nil }
func (nopInput) KeyDown(code uint16) error { return nil }
func (nopInput) KeyUp(code uint16) error   { return nil }

type follower struct {
	mu     sync.Mutex
	events []string
}

func (f *follower) add(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}
func (f *follower) Arrive(side string, y int32) { f.add("arrive " + side) }
func (f *follower) Follow(t Target, side string, y int32) {
	f.add("follow " + t.Name + " " + side)
}
func (f *follower) Lost(name string) { f.add("lost " + name) }
func (f *follower) has(e string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.events, e)
}

type machine struct {
	g    *Group
	f    *follower
	stop chan struct{}
	port int
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func startMachine(t *testing.T, name string, port int, bootstrap string) *machine {
	t.Helper()
	m := &machine{f: &follower{}, stop: make(chan struct{}), port: port}
	incoming, err := network.ListenAndAccept(port, "groupTestKey0123456", name, "127.0.0.1", nil, nil, m.stop)
	if err != nil {
		t.Fatal(err)
	}
	m.g = New(Options{
		Name: name, Key: "groupTestKey0123456", Port: port, Bootstrap: bootstrap,
		Screen:  func() (int32, int32) { return 1600, 900 },
		Sharing: true,
		Session: func(string, *network.Conn) (*network.Handler, func()) {
			return &network.Handler{Mouse: nopInput{}, Keyboard: nopInput{}}, nil
		},
		Cursor: m.f,
	})
	m.g.Start()
	go m.g.Run(incoming, m.stop)
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func onlineCount(g *Group) int {
	n := 0
	for _, m := range g.Snapshot().Members {
		if m.Online && m.Current {
			n++
		}
	}
	return n
}

func TestSnapshotReportsWhichMachineSetTheLayout(t *testing.T) {
	g := New(Options{
		Name:   "me",
		Layout: Layout{Order: []string{"peer", "me"}, Changed: 5, By: "peer"},
	})
	if got := g.Snapshot().LayoutBy; got != "peer" {
		t.Fatalf("Snapshot().LayoutBy = %q, want %q so the GUI can say the order was set elsewhere", got, "peer")
	}
}

func TestNeighborEdgesFromTheGrid(t *testing.T) {
	// A row: this machine in the middle borders left and right only.
	row := New(Options{Name: "me", Layout: Layout{Order: []string{"a", "me", "b"}}})
	if e := row.NeighborEdges(); !e["left"] || !e["right"] || e["up"] || e["down"] {
		t.Fatalf("row edges = %v", e)
	}

	// A 2-D arrangement: a machine below me means a "down" edge, nothing left.
	twoD := New(Options{Name: "me", Layout: Layout{
		Order: []string{"me", "right"},
		Pos:   map[string]Pos{"me": {0, 0}, "right": {1, 0}, "below": {0, 1}},
	}})
	e := twoD.NeighborEdges()
	if !e["right"] || !e["down"] || e["left"] || e["up"] {
		t.Fatalf("2-D edges = %v", e)
	}
}

func TestFileCopyTargetsHybrid(t *testing.T) {
	g := New(Options{Name: "me"})
	a := g.newLinkLocked("a")
	a.conn = &network.Conn{}
	b := g.newLinkLocked("b")
	b.conn = &network.Conn{}
	g.newLinkLocked("c") // offline: no conn

	// Pointer at home, or its place unknown: broadcast to every machine online
	// (a and b), never the offline c.
	for _, loc := range []string{"", "me"} {
		g.loc = loc
		if got := g.FileCopyTargets(); len(got) != 2 {
			t.Fatalf("loc=%q: file copy sent to %d machines, want 2 (broadcast)", loc, len(got))
		}
	}

	// Pointer on a remote machine online: the file goes to just that one.
	g.loc = "a"
	got := g.FileCopyTargets()
	if len(got) != 1 || got[0] != a.conn {
		t.Fatalf("loc=a: targets=%v, want only a's connection", got)
	}

	// Pointer on an offline machine: fall back to broadcast, do not drop the
	// copy on the floor.
	g.loc = "c"
	if got := g.FileCopyTargets(); len(got) != 2 {
		t.Fatalf("loc=c (offline): sent to %d, want 2 (broadcast fallback)", len(got))
	}
}

func TestForgetRemovesMachineAndTombstonesIt(t *testing.T) {
	g := New(Options{Name: "me", Layout: Layout{Order: []string{"me", "gone", "keep"}, Changed: 5, By: "me"}})
	g.newLinkLocked("gone")
	g.newLinkLocked("keep")

	g.Forget("gone")

	if !g.isForgotten("gone") {
		t.Fatal("forgotten machine has no tombstone")
	}
	if _, ok := g.links["gone"]; ok {
		t.Fatal("forgotten machine still has a link")
	}
	snap := g.Snapshot()
	if slices.Contains(snap.Layout, "gone") {
		t.Fatalf("forgotten machine still in the layout: %v", snap.Layout)
	}
	if !slices.Contains(snap.Layout, "keep") {
		t.Fatalf("forgetting one machine dropped another: %v", snap.Layout)
	}
	// Forgetting this machine itself is refused: a group cannot forget its own
	// screen.
	g.Forget("me")
	if g.isForgotten("me") {
		t.Fatal("a machine forgot itself")
	}
}

func TestNewDropsAForgottenMachineFromTheMemberStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "members.json")
	if err := saveMembers(path, map[string]string{"gone": "10.0.0.9", "keep": "10.0.0.2"}); err != nil {
		t.Fatal(err)
	}
	if err := saveForgotten(forgottenPath(path), map[string]int64{"gone": 100}); err != nil {
		t.Fatal(err)
	}

	New(Options{Name: "me", StorePath: path})

	got := loadMembers(path)
	if _, ok := got["gone"]; ok {
		t.Fatalf("forgotten machine left in the member store: %v", got)
	}
	if got["keep"] != "10.0.0.2" {
		t.Fatalf("New dropped a machine it should have kept: %v", got)
	}
}

func TestStartRewritesTheLayoutWithoutAForgottenMachine(t *testing.T) {
	var saved []string
	g := New(Options{
		Name:   "me",
		Layout: Layout{Order: []string{"gone", "me", "keep"}, Changed: 5, By: "old"},
		SaveLayout: func(l Layout) error {
			saved = l.Order
			return nil
		},
	})
	// A tombstone this process already holds (as if loaded from forgotten.json
	// or consumed from a request in New).
	g.forgotten["gone"] = 100

	g.Start()

	if slices.Contains(saved, "gone") || saved == nil {
		t.Fatalf("Start did not rewrite config without the forgotten machine: saved=%v", saved)
	}
	if slices.Contains(g.Snapshot().Layout, "gone") {
		t.Fatalf("forgotten machine still in the layout after Start: %v", g.Snapshot().Layout)
	}
}

func TestGossipCannotResurrectAForgottenMachine(t *testing.T) {
	g := New(Options{Name: "me", Layout: Layout{Order: []string{"me", "peer"}}})
	from := g.newLinkLocked("peer")
	g.Forget("gone") // gone was never a member; the tombstone still stands

	// A peer that still remembers "gone" advertises it, with a newer layout
	// that even lists it. Neither brings it back.
	doc, err := encodeInfo(
		Layout{Order: []string{"me", "peer", "gone"}, Changed: 999, By: "peer"},
		map[string]string{"gone": "10.0.0.9"}, nil,
		1600, 900, true, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.applyInfo(from, doc)

	if _, ok := g.links["gone"]; ok {
		t.Fatal("hearsay re-created the forgotten machine's link")
	}
	if slices.Contains(g.Snapshot().Layout, "gone") {
		t.Fatalf("hearsay put the forgotten machine back in the layout: %v", g.Snapshot().Layout)
	}
}

func TestAStaleTombstoneCannotDropAReconnectedMachine(t *testing.T) {
	// mystogan was forgotten, then powered back on and reconnected here, which
	// lifted its tombstone locally. A peer that still holds the old tombstone
	// gossips it. Because mystogan is connected now, it must not be dropped:
	// presence beats a stale forget, so it does not flap out of the group.
	g := New(Options{Name: "me", Layout: Layout{Order: []string{"peer", "me", "mystogan"}}})
	peer := g.newLinkLocked("peer")
	myst := g.newLinkLocked("mystogan")
	myst.conn = &network.Conn{} // reconnected: live here

	// An empty layout (not newer) so applyInfo makes no layout change and does
	// not broadcast: this test exercises only the tombstone decision, and the
	// fake connection cannot carry a real packet.
	doc, err := encodeInfo(
		Layout{}, nil, map[string]int64{"mystogan": time.Now().Unix()},
		1600, 900, true, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.applyInfo(peer, doc)

	if _, ok := g.links["mystogan"]; !ok {
		t.Fatal("a stale tombstone dropped a machine that is connected now")
	}
	if g.isForgotten("mystogan") {
		t.Fatal("a stale tombstone re-forgot a connected machine")
	}
	if !slices.Contains(g.Snapshot().Layout, "mystogan") {
		t.Fatalf("connected machine fell out of the layout: %v", g.Snapshot().Layout)
	}
}

func TestAdoptingATombstoneDropsAKnownMember(t *testing.T) {
	g := New(Options{Name: "me", Layout: Layout{Order: []string{"me", "peer", "gone"}}})
	from := g.newLinkLocked("peer")
	g.newLinkLocked("gone")

	// peer has forgotten "gone" and says so. This machine adopts it.
	doc, err := encodeInfo(
		Layout{Order: []string{"me", "peer"}, Changed: 10, By: "peer"},
		map[string]string{"gone": "10.0.0.9"},
		map[string]int64{"gone": time.Now().Unix()},
		1600, 900, true, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.applyInfo(from, doc)

	if !g.isForgotten("gone") {
		t.Fatal("did not adopt the peer's tombstone")
	}
	if _, ok := g.links["gone"]; ok {
		t.Fatal("adopting a tombstone left the member's link")
	}
	if slices.Contains(g.Snapshot().Layout, "gone") {
		t.Fatalf("adopting a tombstone left the member in the layout: %v", g.Snapshot().Layout)
	}
}

func TestAdoptedTombstoneForANeighborRestartsCapture(t *testing.T) {
	restarted := false
	g := New(Options{
		Name:             "me",
		Layout:           Layout{Order: []string{"gone", "me", "peer"}},
		NeighborsChanged: func() { restarted = true },
	})
	from := g.newLinkLocked("peer")
	g.newLinkLocked("gone")

	// peer forgets "gone", this machine's left neighbour. The shared edge on
	// the left disappears, so the capturer must be told to stop watching it.
	doc, err := encodeInfo(
		Layout{Order: []string{"me", "peer"}, Changed: 10, By: "peer"},
		nil, map[string]int64{"gone": time.Now().Unix()},
		1600, 900, true, 0, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.applyInfo(from, doc)

	if !restarted {
		t.Fatal("dropping a neighbour via a tombstone did not restart capture")
	}
	if g.NeighborEdges()["left"] {
		t.Fatal("still reports a left neighbour after the only one was forgotten")
	}
}

func TestThreeMachinesJoinThroughOneAndShareTheCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("opens real connections")
	}
	pa, pb, pc := freePort(t), freePort(t), freePort(t)
	a := startMachine(t, "alpha", pa, "")
	defer close(a.stop)
	addrA := "127.0.0.1:" + strconv.Itoa(pa)
	b := startMachine(t, "bravo", pb, addrA)
	defer close(b.stop)
	c := startMachine(t, "charlie", pc, addrA)

	// Only alpha's address was given; bravo and charlie find each other.
	for _, m := range []*machine{a, b, c} {
		waitFor(t, m.g.o.Name+" connected to both others", func() bool { return onlineCount(m.g) == 2 })
	}
	want := []string{"alpha", "bravo", "charlie"}
	for _, m := range []*machine{a, b, c} {
		waitFor(t, m.g.o.Name+" sharing one layout", func() bool { return slices.Equal(m.g.Snapshot().Layout, want) })
	}

	// alpha's input moves the cursor onto charlie: everyone follows.
	tgt, self, ok := a.g.Beyond("bravo", "right")
	if !ok || self || tgt.Name != "charlie" || tgt.Width != 1600 {
		t.Fatalf("Beyond(bravo, right) = %+v self=%v ok=%v", tgt, self, ok)
	}
	a.g.Moved("charlie", "left", 1000)
	waitFor(t, "charlie told the cursor arrived", func() bool { return c.f.has("arrive left") })
	waitFor(t, "bravo following to charlie", func() bool { return b.f.has("follow charlie left") })
	if a.g.Snapshot().Location != "charlie" || b.g.Snapshot().Location != "charlie" {
		t.Fatal("location not recorded")
	}

	// A late, older announcement changes nothing.
	b.g.mu.Lock()
	b.g.epoch = 0
	b.g.mu.Unlock()

	// charlie drops off: the others take the cursor back and skip its screen.
	close(c.stop)
	waitFor(t, "bravo noticing charlie left", func() bool { return b.f.has("lost charlie") })
	waitFor(t, "alpha noticing charlie left", func() bool { return onlineCount(a.g) == 1 })
	if _, _, ok := a.g.Beyond("bravo", "right"); ok {
		t.Fatal("an offline machine is still reachable")
	}
	if a.g.Snapshot().Location != "" {
		t.Fatal("the cursor's location should be unknown once its machine left")
	}
}

// TestLinkReconnectsAfterItsStoredAddressGoesStale reproduces the failure where
// a machine would not rejoin after a drop until it was manually restarted: its
// link held a stale address (the network had moved and the peer's IP changed),
// so the link redialed that dead address forever while bootstrap deferred to
// it. A successful connection now refreshes the link's redial address, and
// bootstrap resumes dialing the configured address whenever the member is
// offline, so recovery no longer needs a restart.
func TestLinkReconnectsAfterItsStoredAddressGoesStale(t *testing.T) {
	if testing.Short() {
		t.Skip("opens real connections")
	}
	pa, pb := freePort(t), freePort(t)
	a := startMachine(t, "alpha", pa, "")
	defer close(a.stop)
	addrA := "127.0.0.1:" + strconv.Itoa(pa)
	b := startMachine(t, "bravo", pb, addrA)
	defer close(b.stop)

	waitFor(t, "bravo connected to alpha", func() bool { return onlineCount(b.g) == 1 })

	// The network moves: poison the learned address with an unroutable one and
	// drop the link, as a changed IP would. The link now holds a stale address
	// that no longer reaches alpha — the exact state that used to wedge it.
	b.g.mu.Lock()
	l := b.g.links["alpha"]
	l.addr = "203.0.113.1" // TEST-NET-3: guaranteed unroutable
	c := l.conn
	b.g.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}

	// Recovery must not depend on the stale address: bootstrap re-dials the
	// configured one.
	waitFor(t, "bravo sees the drop", func() bool { return onlineCount(b.g) == 0 })
	waitFor(t, "bravo reconnects to alpha without a restart", func() bool { return onlineCount(b.g) == 1 })

	// And the successful reconnection refreshed the redial address off the
	// stale value, so a further drop would dial somewhere that still works.
	b.g.mu.Lock()
	addr := b.g.links["alpha"].addr
	b.g.mu.Unlock()
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host != "127.0.0.1" {
		t.Fatalf("after reconnecting, link address = %q; a successful connection must refresh the stale address", addr)
	}
}
