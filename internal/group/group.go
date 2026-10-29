package group

import (
	"log/slog"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const (
	dialTimeout    = 10 * time.Second
	redialDelay    = 2 * time.Second
	bootstrapIdle  = 5 * time.Second
	defaultWidth   = 1920
	defaultHeight  = 1080
	incomingBuffer = 2
)

// Target is a machine the cursor can move to.
type Target struct {
	Name          string
	Conn          *network.Conn
	Width, Height int32
}

// CursorFollower applies cursor moves that other machines announce. The
// capturer implements it; a machine that does not share its input has none.
type CursorFollower interface {
	// Arrive: the cursor is now on this machine, entering from side at
	// height y (0-65535).
	Arrive(side string, y int32)
	// Follow: the cursor is now on t, entering from side at height y. Local
	// input goes there until it moves again.
	Follow(t Target, side string, y int32)
	// Lost: the connection to the named machine ended. If the cursor was
	// there, it comes back here.
	Lost(name string)
}

// Options configures a Group.
type Options struct {
	Name string // this machine
	Key  string
	Port int
	// Bootstrap is any member's address, used until that member is known.
	// Empty means waiting for the others to connect.
	Bootstrap string
	// Layout as saved, and the two-machine edge for a setup that predates
	// layouts.
	Layout     Layout
	LegacyEdge string
	// SaveLayout persists a layout that changed; nil keeps it in memory.
	SaveLayout func(Layout) error
	// StorePath keeps learned member addresses; "" keeps them in memory.
	StorePath string
	// Screen reports this machine's screen size, 0×0 if unknown.
	Screen  func() (w, h int32)
	Sharing bool
	// Session prepares one connection: the handler its packets go to, and a
	// function to run when it ends.
	Session func(name string, conn *network.Conn) (*network.Handler, func())
	// Cursor follows moves other machines announce; nil when not sharing.
	Cursor CursorFollower
	// Changed runs after anything worth showing changed: members, layout or
	// where the cursor is.
	Changed func()
	// NeighborsChanged runs when a new layout gives this machine a neighbour
	// on a different set of sides, so the edges watched must change.
	NeighborsChanged func()
}

type link struct {
	name string
	// Guarded by Group.mu.
	addr          string
	conn          *network.Conn
	since         time.Time // when conn connected
	info          bool
	width, height int32
	sharing       bool

	incoming chan *network.Conn // dialed by the other machine
	dialed   chan *network.Conn // dialed here, outside the link's own loop
	kick     chan struct{}      // an address was learned: dial it
}

// Group runs this machine's membership in a group.
type Group struct {
	o Options

	mu        sync.Mutex
	links     map[string]*link
	layout    Layout
	forgotten map[string]int64 // name -> Unix second it was forgotten
	epoch     uint32
	loc       string // where the cursor is; "" = unknown
	locBy     string // who announced it
	msg       byte
	bootName  string // the member the bootstrap address reached
	running   bool
	stop      <-chan struct{}
}

// New prepares a group; Run starts it.
func New(o Options) *Group {
	g := &Group{o: o, links: map[string]*link{}, layout: o.Layout}
	g.forgotten = loadForgotten(forgottenPath(o.StorePath))
	// A machine the front end asked to forget joins the tombstones now, so it
	// is dropped below and its link is never made.
	now := time.Now().Unix()
	saveNeeded := false
	for _, name := range takeForgetRequest(requestPath(o.StorePath)) {
		if name != o.Name {
			g.forgotten[name] = now
			saveNeeded = true
		}
	}
	staleMember := false
	for name, addr := range loadMembers(o.StorePath) {
		if name == o.Name {
			continue
		}
		if g.isForgotten(name) {
			staleMember = true // a tombstoned machine still in the member store
			continue
		}
		if l := g.newLinkLocked(name); l != nil {
			l.addr = addr
		}
	}
	// A consumed request is new: persist the tombstone. Either way, rewrite the
	// member store when it still lists a forgotten machine, so it is not left as
	// a stale entry. The saved layout is trimmed in Start, where the change can
	// be written to config and announced. (Start always runs before Run.)
	if saveNeeded {
		capForgotten(g.forgotten)
		_ = saveForgotten(forgottenPath(o.StorePath), g.forgotten)
	}
	if saveNeeded || staleMember {
		g.saveMembers()
	}
	return g
}

// isForgotten reports whether name carries a tombstone. g.mu held, or during
// New before Run.
func (g *Group) isForgotten(name string) bool {
	_, ok := g.forgotten[name]
	return ok
}

// dropForgottenPos removes tombstoned machines from a position map, returning
// nil when nothing is left. A row layout (nil Pos) is unaffected.
func (g *Group) dropForgottenPos(pos map[string]Pos) map[string]Pos {
	if len(pos) == 0 || len(g.forgotten) == 0 {
		return pos
	}
	out := make(map[string]Pos, len(pos))
	for n, p := range pos {
		if !g.isForgotten(n) {
			out[n] = p
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// withoutForgotten returns order with every tombstoned name removed.
func (g *Group) withoutForgotten(order []string) []string {
	if len(g.forgotten) == 0 {
		return order
	}
	out := order[:0:0]
	for _, n := range order {
		if !g.isForgotten(n) {
			out = append(out, n)
		}
	}
	return out
}

// capForgotten keeps the tombstone set within maxForgotten by dropping the
// oldest, so it cannot grow without bound over a long-lived install.
func capForgotten(forgotten map[string]int64) {
	for len(forgotten) > maxForgotten {
		var oldest string
		var at int64
		for n, t := range forgotten {
			if oldest == "" || t < at {
				oldest, at = n, t
			}
		}
		delete(forgotten, oldest)
	}
}

// Run connects to the members, accepts their connections from incoming, and
// returns when stop is closed.
func (g *Group) Run(incoming <-chan *network.Conn, stop <-chan struct{}) {
	g.mu.Lock()
	g.running, g.stop = true, stop
	links := slices.Collect(maps.Values(g.links))
	g.mu.Unlock()
	for _, l := range links {
		go g.runLink(l)
	}
	if g.o.Bootstrap != "" {
		go g.bootstrap()
	}
	for {
		select {
		case <-stop:
			g.mu.Lock()
			for _, l := range g.links {
				if l.conn != nil {
					_ = l.conn.Close()
				}
			}
			g.mu.Unlock()
			return
		case conn, ok := <-incoming:
			if !ok {
				<-stop
				return
			}
			g.route(conn, false)
		}
	}
}

// route hands a new connection to the link for the machine it reached.
func (g *Group) route(conn *network.Conn, outbound bool) string {
	name := strings.TrimRight(conn.RemoteName, " ")
	if name == g.o.Name || !validName(name) {
		slog.Warn("group: a machine connected with this machine's own name, or none; give each machine its own name",
			"name", name)
		_ = conn.Close()
		return ""
	}
	g.mu.Lock()
	l := g.links[name]
	if l == nil {
		l = g.newLinkLocked(name)
		if l != nil && g.running {
			go g.runLink(l)
		}
	}
	g.mu.Unlock()
	if l == nil {
		slog.Warn("group: refusing a machine, the group is full", "name", name, "max", MaxMachines)
		_ = conn.Close()
		return ""
	}
	ch := l.incoming
	if outbound {
		ch = l.dialed
	}
	select {
	case ch <- conn:
	default:
		_ = conn.Close()
	}
	return name
}

// newLinkLocked adds a member; nil when the group is full. g.mu held.
func (g *Group) newLinkLocked(name string) *link {
	if len(g.links) >= MaxMachines-1 {
		return nil
	}
	l := &link{
		name:     name,
		incoming: make(chan *network.Conn, incomingBuffer),
		dialed:   make(chan *network.Conn, incomingBuffer),
		kick:     make(chan struct{}, 1),
	}
	g.links[name] = l
	return l
}

func (g *Group) stopped() bool {
	select {
	case <-g.stop:
		return true
	default:
		return false
	}
}

// hostPort adds the group's port to addr unless it names one already.
func (g *Group) hostPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, strconv.Itoa(g.o.Port))
}

// runLink keeps one member connected for as long as the group runs.
func (g *Group) runLink(l *link) {
	for !g.stopped() {
		g.mu.Lock()
		addr := l.addr
		g.mu.Unlock()
		dialStop := make(chan struct{})
		var out <-chan *network.Conn
		if addr != "" {
			out = network.ConnectWithRetry(g.hostPort(addr), g.o.Key, g.o.Name, dialTimeout, redialDelay, dialStop)
		}
		conn, outbound := g.pick(l, out)
		close(dialStop)
		if conn == nil {
			continue // stopped, or an address to dial was learned
		}
		g.session(l, conn, outbound)
		select {
		case <-g.stop:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// pick waits for a connection to l and keeps the one both machines agree
// on: the one dialed by the machine whose name sorts first, or whichever
// exists after a grace period (see network.SelectLinuxPeer).
func (g *Group) pick(l *link, out <-chan *network.Conn) (*network.Conn, bool) {
	var first *network.Conn
	var firstOut bool
	var grace <-chan time.Time
	prefer, decidable := network.PreferOutbound(g.o.Name, l.name)

	consider := func(c *network.Conn, outbound bool) (*network.Conn, bool, bool) {
		if c == nil {
			return nil, false, false
		}
		if name := strings.TrimRight(c.RemoteName, " "); name != l.name {
			// The address now belongs to another machine, or to this one.
			slog.Debug("group: address reached a different machine", "want", l.name, "got", name)
			if name == g.o.Name {
				_ = c.Close()
			} else {
				g.route(c, outbound)
			}
			return nil, false, false
		}
		if first == nil {
			if !decidable || prefer == outbound {
				return c, outbound, true
			}
			first, firstOut = c, outbound
			grace = time.After(network.LinuxPeerGrace)
			return nil, false, false
		}
		if outbound == firstOut {
			_ = c.Close() // a duplicate in the same direction
			return nil, false, false
		}
		_ = first.Close() // c is the preferred direction
		return c, outbound, true
	}

	for {
		var c *network.Conn
		var outbound, done bool
		select {
		case <-g.stop:
			if first != nil {
				_ = first.Close()
			}
			return nil, false
		case <-l.kick:
			if first != nil {
				return first, firstOut
			}
			return nil, false
		case conn, ok := <-out:
			if !ok {
				out = nil
				continue
			}
			c, outbound, done = consider(conn, true)
		case conn := <-l.dialed:
			c, outbound, done = consider(conn, true)
		case conn := <-l.incoming:
			c, outbound, done = consider(conn, false)
		case <-grace:
			return first, firstOut
		}
		if done {
			return c, outbound
		}
	}
}

// session runs one connection to a member until it ends.
func (g *Group) session(l *link, conn *network.Conn, outbound bool) {
	conn.SetIdleTimeout(network.LinuxPeerIdleTimeout)
	// Close the duplicates that arrived while picking. Left open, one may be
	// the socket the other machine kept, and each side would wait on a
	// different socket until the idle timeout.
	for drained := false; !drained; {
		select {
		case c := <-l.incoming:
			_ = c.Close()
		case c := <-l.dialed:
			_ = c.Close()
		default:
			drained = true
		}
	}
	g.mu.Lock()
	l.conn, l.info, l.since = conn, false, time.Now()
	learned := false
	// Keep the address this link redials fresh. It may hold a stale address
	// from members.json — the network moved and the peer's IP changed — while
	// the connection that just succeeded names the peer's current address,
	// whichever side dialed. Adopt it, so that when this link drops it redials
	// an address that still reaches the peer instead of the old one forever.
	if host := conn.RemoteHost(); host != "" && host != l.addr {
		l.addr = host
		learned = true
	}
	// A forgotten machine that connects here in person is back: presence is
	// newer than any tombstone, so lift it. It rejoins the layout through the
	// usual completion once its group document arrives.
	resurrected := false
	if _, ok := g.forgotten[l.name]; ok {
		delete(g.forgotten, l.name)
		resurrected = true
	}
	g.mu.Unlock()
	if resurrected {
		slog.Info("group: a forgotten machine reconnected and rejoined the group", "machine", l.name)
		_ = saveForgotten(forgottenPath(g.o.StorePath), g.forgottenCopy())
	}
	if learned {
		g.saveMembers()
	}
	slog.Info("group: connected", "machine", l.name, "outbound", outbound)

	h, end := g.o.Session(l.name, conn)
	var asm protocol.GroupAssembler
	h.OnGroup = func(p *protocol.Packet) {
		switch p.Type {
		case protocol.LinuxGroup:
			if doc, ok := asm.Add(p); ok {
				g.applyInfo(l, doc)
			}
		case protocol.LinuxCursor:
			if c, ok := p.CursorAt(); ok {
				g.applyCursor(l, h, c)
			}
		}
	}
	g.sendInfo(conn)
	g.changed()
	// A member counts as current until infoGrace has passed without its
	// group document; show the verdict then.
	recheck := time.AfterFunc(infoGrace, g.changed)
	defer recheck.Stop()

	if err := network.ReceiveLoop(conn, h); err != nil {
		slog.Info("group: connection ended", "machine", l.name, "err", err)
	}
	if end != nil {
		end()
	}
	g.mu.Lock()
	if l.conn == conn {
		l.conn, l.info = nil, false
	}
	if g.loc == l.name {
		g.loc = ""
	}
	g.mu.Unlock()
	_ = conn.Close()
	if g.o.Cursor != nil {
		g.o.Cursor.Lost(l.name)
	}
	slog.Info("group: disconnected", "machine", l.name)
	g.changed()
}

// bootstrap dials the configured address until it has found which member
// it belongs to; from then on that member's link dials it.
func (g *Group) bootstrap() {
	for !g.stopped() {
		g.mu.Lock()
		// "found" means the bootstrap member is connected now, not merely that a
		// link object exists for it. If it exists but is offline, keep dialing
		// the configured address: that address is the one the user set and is
		// current, so it recovers the connection even when the link is sitting
		// on a stale learned address.
		found := g.bootName != "" && g.links[g.bootName] != nil && g.links[g.bootName].conn != nil
		if !found {
			// A remembered member at this address is dialed by its own
			// link; a second dial would only race it. If the address has
			// changed hands, that link passes the connection on.
			for _, l := range g.links {
				if l.addr != "" && g.hostPort(l.addr) == g.hostPort(g.o.Bootstrap) {
					g.bootName, found = l.name, true
					break
				}
			}
		}
		g.mu.Unlock()
		if found {
			g.sleep(bootstrapIdle)
			continue
		}
		conn, err := network.Connect(g.hostPort(g.o.Bootstrap), g.o.Key, g.o.Name, dialTimeout)
		if err != nil {
			slog.Debug("group: bootstrap address not reachable yet", "host", g.o.Bootstrap, "err", err)
			g.sleep(redialDelay)
			continue
		}
		if routed := g.route(conn, true); routed != "" {
			g.mu.Lock()
			g.bootName = routed
			if l := g.links[routed]; l != nil && l.addr == "" {
				l.addr = g.o.Bootstrap
			}
			g.mu.Unlock()
			g.saveMembers()
		} else {
			g.sleep(redialDelay)
		}
	}
}

func (g *Group) sleep(d time.Duration) {
	select {
	case <-g.stop:
	case <-time.After(d):
	}
}

// sendInfo sends this machine's group document over conn.
func (g *Group) sendInfo(conn *network.Conn) {
	var w, h int32
	if g.o.Screen != nil {
		w, h = g.o.Screen()
	}
	g.mu.Lock()
	members := map[string]string{}
	for name, l := range g.links {
		if l.addr != "" {
			members[name] = l.addr
		}
	}
	forgotten := maps.Clone(g.forgotten)
	doc, err := encodeInfo(g.layout, members, forgotten, w, h, g.o.Sharing, g.epoch, g.o.Port, time.Now())
	g.msg++
	msg := g.msg
	g.mu.Unlock()
	if err != nil {
		slog.Warn("group: could not encode the group document", "err", err)
		return
	}
	pkts, err := protocol.GroupChunks(conn.MachineID, conn.RemoteID, msg, doc)
	if err != nil {
		slog.Warn("group: group document too large", "bytes", len(doc), "err", err)
		return
	}
	for _, p := range pkts {
		if err := conn.SendPacket(p); err != nil {
			slog.Debug("group: send group document", "err", err)
			return
		}
	}
}

func (g *Group) onlineConns() []*network.Conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []*network.Conn
	for _, l := range g.links {
		if l.conn != nil {
			out = append(out, l.conn)
		}
	}
	return out
}

// Conns returns the connections to every member online now.
func (g *Group) Conns() []*network.Conn { return g.onlineConns() }

// FileCopyTargets returns the connections a copied file should be sent to: the
// single machine the cursor is on, when that is a remote machine online now,
// otherwise every machine online. So a large file travels once, to where the
// pointer is, instead of to the whole group; but a copy made with the pointer
// at home (or its place unknown) still reaches everyone, matching how clipboard
// text and images are always broadcast.
func (g *Group) FileCopyTargets() []*network.Conn {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.loc != "" && g.loc != g.o.Name {
		if l := g.links[g.loc]; l != nil && l.conn != nil {
			return []*network.Conn{l.conn}
		}
	}
	var out []*network.Conn
	for _, l := range g.links {
		if l.conn != nil {
			out = append(out, l.conn)
		}
	}
	return out
}

// applyInfo takes a member's group document.
func (g *Group) applyInfo(from *link, doc []byte) {
	in, theirs, err := decodeInfo(doc, time.Now())
	if err != nil {
		slog.Warn("group: ignoring a group document", "machine", from.name, "err", err)
		return
	}
	me := g.o.Name
	g.mu.Lock()
	from.info, from.width, from.height, from.sharing = true, in.Width, in.Height, in.Sharing
	g.epoch = max(g.epoch, in.Epoch)

	// Capture the layout and this machine's neighbours before anything below
	// changes them, so layoutUpdated still restarts capture when an adopted
	// tombstone drops this machine's neighbour, not only when the order moves.
	before := g.layout
	leftBefore, rightBefore := Neighbors(before.Order, me)

	// Adopt tombstones the sender holds and this machine does not, or holds
	// more recently, and drop any member they name. A machine forgotten on one
	// screen is forgotten on all, so a peer that has not heard yet cannot keep
	// re-advertising it. The machine's own name is never a tombstone.
	forgetChanged := false
	for name, at := range in.Forgotten {
		if name == me {
			continue
		}
		if l := g.links[name]; l != nil && l.conn != nil {
			// Connected right now: the machine is plainly present, so a
			// tombstone from a peer that has not yet seen it reconnect does not
			// drop it. Presence beats a stale forget, exactly as a reconnection
			// lifts this machine's own tombstone (see session). Without this a
			// peer's old tombstone would re-forget a machine that just rejoined,
			// flapping it in and out of the group.
			continue
		}
		if cur, ok := g.forgotten[name]; !ok || at > cur {
			g.forgotten[name] = at
			forgetChanged = true
			delete(g.links, name) // offline here: no live connection to close
		}
	}
	if forgetChanged {
		capForgotten(g.forgotten)
		g.layout.Order = g.withoutForgotten(g.layout.Order)
		g.layout.Pos = g.dropForgottenPos(g.layout.Pos)
	}

	var start []*link
	learned := false
	if in.Port > 0 && in.Port != g.o.Port && from.addr != "" {
		// It listens on a port of its own: dial that one.
		if _, _, err := net.SplitHostPort(from.addr); err != nil {
			from.addr = net.JoinHostPort(from.addr, strconv.Itoa(in.Port))
			learned = true
		}
	}
	for name, addr := range in.Members {
		if name == me || g.isForgotten(name) {
			// Hearsay about a forgotten machine does not bring it back; only a
			// direct connection from the machine itself does (see session).
			continue
		}
		l := g.links[name]
		if l == nil {
			if l = g.newLinkLocked(name); l == nil {
				slog.Warn("group: not adding a member, the group is full", "name", name, "max", MaxMachines)
				continue
			}
			start = append(start, l)
		}
		// An offline member may have moved: take the newer report.
		if addr != "" && addr != l.addr && (l.addr == "" || l.conn == nil) {
			l.addr = addr
			learned = true
			select {
			case l.kick <- struct{}{}:
			default:
			}
		}
	}

	if len(theirs.Order) > 0 && theirs.Newer(g.layout) {
		g.layout = theirs
		g.layout.Order = g.withoutForgotten(theirs.Order)
		g.layout.Pos = g.dropForgottenPos(theirs.Pos)
	}
	bumped := g.completeLocked()
	after := g.layout
	running := g.running
	g.mu.Unlock()

	if running {
		for _, l := range start {
			go g.runLink(l)
		}
	}
	if forgetChanged {
		_ = saveForgotten(forgottenPath(g.o.StorePath), g.forgottenCopy())
	}
	if learned || len(start) > 0 {
		g.saveMembers()
	}
	g.layoutUpdated(before, after, leftBefore, rightBefore, bumped)
	g.changed()
}

// forgottenCopy returns a snapshot of the tombstones for writing to disk.
func (g *Group) forgottenCopy() map[string]int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return maps.Clone(g.forgotten)
}

// completeLocked adds members missing from the layout. Adding another
// machine is a new choice the others must hear about; adding only this one
// is not. Reports whether this machine made a new choice. g.mu held.
func (g *Group) completeLocked() bool {
	names := slices.Collect(maps.Keys(g.links))
	slices.Sort(names)
	order, added := Complete(g.layout.Order, names, g.o.Name, g.o.LegacyEdge)
	if !added {
		return false
	}
	others := false
	for _, n := range order {
		if n != g.o.Name && !slices.Contains(g.layout.Order, n) {
			others = true
		}
	}
	g.layout.Order = order
	if others {
		g.layout.Changed, g.layout.By = time.Now().Unix(), g.o.Name
	}
	return others
}

// layoutUpdated saves and announces a layout that changed.
func (g *Group) layoutUpdated(before, after Layout, leftBefore, rightBefore, originated bool) {
	if after.Equal(before) {
		return
	}
	slog.Info("group: screen order", "layout", after.Order, "by", after.By)
	if g.o.SaveLayout != nil && after.Changed > 0 {
		if err := g.o.SaveLayout(after); err != nil {
			slog.Warn("group: could not save the screen order", "err", err)
		}
	}
	if originated {
		for _, c := range g.onlineConns() {
			g.sendInfo(c)
		}
	}
	left, right := Neighbors(after.Order, g.o.Name)
	if left != leftBefore || right != rightBefore {
		// A side change this machine did not originate came from the machine
		// in after.By adopting its screen order. Say so plainly: otherwise the
		// cursor silently stops crossing the edge the user expects, with no
		// hint that the shared edge moved to the other side.
		if !originated && after.By != "" && after.By != g.o.Name {
			slog.Info("group: adopted the screen order from another machine; your shared edge moved to match it",
				"by", after.By, "order", after.Order, "left", left, "right", right)
		}
		if g.o.NeighborsChanged != nil {
			g.o.NeighborsChanged()
		}
	}
}

// Start completes the saved layout with the members already known, as
// Run's first step would; call before Run so the capturer sees the result.
func (g *Group) Start() {
	g.mu.Lock()
	before := g.layout
	l, r := Neighbors(before.Order, g.o.Name)
	// Drop any machine forgotten before Run (a request consumed in New, or a
	// tombstone loaded from disk) from the saved order. Make it a change this
	// machine originated, so the trimmed order is written to config and told to
	// the others, instead of a forgotten screen lingering in the row.
	stripped := g.withoutForgotten(before.Order)
	removed := len(stripped) != len(before.Order)
	if removed {
		g.layout.Order = stripped
		g.layout.Pos = g.dropForgottenPos(g.layout.Pos)
		g.layout.Changed, g.layout.By = time.Now().Unix(), g.o.Name
	}
	bumped := g.completeLocked()
	after := g.layout
	g.mu.Unlock()
	g.layoutUpdated(before, after, l, r, bumped || removed)
}

// applyCursor follows a member's announcement of where the cursor is.
func (g *Group) applyCursor(from *link, h *network.Handler, c protocol.CursorAt) {
	me := g.o.Name
	g.mu.Lock()
	if c.Epoch < g.epoch || (c.Epoch == g.epoch && from.name >= g.locBy) {
		g.mu.Unlock()
		return // older news, or the same move announced twice
	}
	target := ""
	if protocol.NameHash(me) == c.Target {
		target = me
	}
	for name := range g.links {
		if protocol.NameHash(name) == c.Target {
			target = name
		}
	}
	if target == "" {
		g.mu.Unlock()
		return
	}
	g.epoch, g.loc, g.locBy = c.Epoch, target, from.name
	var t Target
	if target != me {
		tl := g.links[target]
		t = Target{Name: target, Conn: tl.conn, Width: orDefault(tl.width, defaultWidth), Height: orDefault(tl.height, defaultHeight)}
	}
	g.mu.Unlock()

	slog.Info("group: cursor moved", "to", target, "by", from.name, "epoch", c.Epoch)
	if target == me {
		h.Reseed() // the announcer's input comes on this connection next
		if g.o.Cursor != nil {
			g.o.Cursor.Arrive(c.Side, c.Y)
		}
	} else if g.o.Cursor != nil && t.Conn != nil {
		g.o.Cursor.Follow(t, c.Side, c.Y)
	}
	g.changed()
}

func orDefault(v, d int32) int32 {
	if v > 0 {
		return v
	}
	return d
}

// Moved announces that this machine's input moved the cursor to the machine
// named to ("" = this one), entering its screen from side at height y.
func (g *Group) Moved(to, side string, y int32) {
	g.mu.Lock()
	if to == "" {
		to = g.o.Name
	}
	g.epoch++
	g.loc, g.locBy = to, g.o.Name
	c := protocol.CursorAt{Epoch: g.epoch, Target: protocol.NameHash(to), Side: side, Y: y}
	g.mu.Unlock()
	for _, conn := range g.onlineConns() {
		if err := conn.SendPacket(protocol.NewCursorPacket(conn.MachineID, conn.RemoteID, c)); err != nil {
			slog.Debug("group: announce cursor", "err", err)
		}
	}
	g.changed()
}

// Forget removes a machine from this group and records a tombstone, so a peer
// that still remembers it cannot gossip it back. The tombstone propagates to
// the whole group, and a layout without the machine goes with it. The machine
// rejoins only by connecting here itself (see session). Forgetting an unknown
// or already-forgotten machine still stamps a fresh tombstone, which is how a
// front-end request for an offline machine takes effect.
func (g *Group) Forget(name string) {
	if name == "" || name == g.o.Name {
		return
	}
	g.mu.Lock()
	now := time.Now().Unix()
	if cur, ok := g.forgotten[name]; !ok || now > cur {
		g.forgotten[name] = now
	}
	capForgotten(g.forgotten)
	if l := g.links[name]; l != nil {
		if l.conn != nil {
			_ = l.conn.Close()
		}
		delete(g.links, name)
	}
	if g.loc == name {
		g.loc = ""
	}
	before := g.layout
	leftBefore, rightBefore := Neighbors(before.Order, g.o.Name)
	g.layout.Order = g.withoutForgotten(g.layout.Order)
	g.layout.Pos = g.dropForgottenPos(g.layout.Pos)
	removed := !slices.Equal(before.Order, g.layout.Order)
	if removed {
		g.layout.Changed, g.layout.By = now, g.o.Name
	}
	after := g.layout
	conns := make([]*network.Conn, 0, len(g.links))
	for _, l := range g.links {
		if l.conn != nil {
			conns = append(conns, l.conn)
		}
	}
	g.mu.Unlock()

	slog.Info("group: forgetting a machine; it will not come back unless it connects here itself", "machine", name)
	_ = saveForgotten(forgottenPath(g.o.StorePath), g.forgottenCopy())
	g.saveMembers()
	// layoutUpdated broadcasts when the order changed and this machine made the
	// change; when the machine was not in the layout, send the tombstone anyway
	// so the others still drop it.
	g.layoutUpdated(before, after, leftBefore, rightBefore, removed)
	if !removed {
		for _, c := range conns {
			g.sendInfo(c)
		}
	}
	g.changed()
}

// Beyond returns the machine past side of the machine named from ("" = this
// one), skipping members that are offline. self reports this machine.
func (g *Group) Beyond(from, side string) (t Target, self, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	me := g.o.Name
	if from == "" {
		from = me
	}
	online := func(n string) bool {
		if n == me {
			return true
		}
		l := g.links[n]
		return l != nil && l.conn != nil
	}
	name, ok := BeyondGrid(g.layout.Grid(), online, from, side)
	if !ok {
		return Target{}, false, false
	}
	if name == me {
		return Target{Name: me}, true, true
	}
	l := g.links[name]
	return Target{Name: name, Conn: l.conn, Width: orDefault(l.width, defaultWidth), Height: orDefault(l.height, defaultHeight)}, false, true
}

// NeighborEdges reports every side of this machine's screen that borders
// another machine in the arrangement, online or not: "left", "right", "up",
// "down". The capturer watches exactly these edges.
func (g *Group) NeighborEdges() map[string]bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Edges(g.layout.Grid(), g.o.Name)
}

func (g *Group) saveMembers() {
	g.mu.Lock()
	members := map[string]string{}
	for name, l := range g.links {
		if l.addr != "" {
			members[name] = l.addr
		}
	}
	g.mu.Unlock()
	if err := saveMembers(g.o.StorePath, members); err != nil {
		slog.Warn("group: could not save member addresses", "err", err)
	}
}

func (g *Group) changed() {
	if g.o.Changed != nil {
		g.o.Changed()
	}
}

// infoGrace is how long a newly connected member has to send its group
// document before it is reported as an older mwb. The document follows the
// handshake at once; the grace covers a slow or busy link.
const infoGrace = 10 * time.Second

// Member is one other machine as the status file shows it.
type Member struct {
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Address string `json:"address,omitempty"`
	Width   int32  `json:"width,omitempty"`
	Height  int32  `json:"height,omitempty"`
	Sharing bool   `json:"sharing"`
	// Current is false for a member online with an older mwb that does not
	// take part in the group.
	Current bool `json:"current"`
}

// Snapshot is the group's state for display.
type Snapshot struct {
	Layout   []string
	Location string
	Members  []Member
	// Pos is the two-dimensional arrangement, when the group has one; nil for a
	// plain row (then Layout is the arrangement). The front end draws from it.
	Pos map[string]Pos
	// LayoutBy names the machine whose screen order the group is using, so the
	// front end can show when the order (and thus the shared edge) was set on
	// another machine rather than here. Empty when unknown.
	LayoutBy string
}

// Snapshot returns the current state, members in layout order.
func (g *Group) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Snapshot{Layout: slices.Clone(g.layout.Order), Location: g.loc, LayoutBy: g.layout.By}
	if len(g.layout.Pos) > 0 {
		// Grid(), not the raw Pos, so a machine that joined after the
		// arrangement was set is included with its assigned cell.
		s.Pos = g.layout.Grid()
	}
	names := slices.Collect(maps.Keys(g.links))
	slices.SortFunc(names, func(a, b string) int {
		ia, ib := slices.Index(g.layout.Order, a), slices.Index(g.layout.Order, b)
		if ia < 0 {
			ia = MaxMachines
		}
		if ib < 0 {
			ib = MaxMachines
		}
		if ia != ib {
			return ia - ib
		}
		return strings.Compare(a, b)
	})
	for _, n := range names {
		l := g.links[n]
		s.Members = append(s.Members, Member{
			Name: n, Online: l.conn != nil, Address: l.addr,
			Width: l.width, Height: l.height, Sharing: l.sharing,
			Current: l.info || (l.conn != nil && time.Since(l.since) < infoGrace),
		})
	}
	return s
}
