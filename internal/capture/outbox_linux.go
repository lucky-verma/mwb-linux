//go:build linux

package capture

import (
	"log/slog"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// Input bound for another machine goes out through an outbox, so the event
// path never waits on the network, and a stalled network never builds a
// backlog of moves.
//
// Pointer moves are absolute, so only the newest one matters. A move waits
// in a single slot, replaced by each newer one, and is written once the
// kernel has sent what it already holds for that connection. When Wi-Fi
// stalls, the moves made meanwhile collapse into one, and the pointer goes
// straight to where the mouse is instead of replaying the stall in slow
// motion. Everything else (buttons, wheel, keys) is queued in order and
// never dropped; a move made before one of them is sent before it.
const (
	// moveBacklog is how many unsent bytes a connection may hold before a
	// new move waits: a few packets, so a healthy link is never held up.
	moveBacklog = 256
	// moveRecheck is how often a waiting move checks the backlog again.
	moveRecheck = 2 * time.Millisecond
)

type outItem struct {
	pkt  *protocol.Packet
	conn *network.Conn                // the destination, for its backlog
	send func(*protocol.Packet) error // nil sends through conn
}

type outbox struct {
	mu    sync.Mutex
	queue []outItem
	move  *outItem
	wake  chan struct{}
}

func newOutbox() *outbox {
	return &outbox{wake: make(chan struct{}, 1)}
}

// put hands pkt to the sender; a move replaces any move still waiting.
func (o *outbox) put(pkt *protocol.Packet, conn *network.Conn, send func(*protocol.Packet) error, move bool) {
	o.mu.Lock()
	it := outItem{pkt: pkt, conn: conn, send: send}
	if move {
		o.move = &it
	} else {
		// A move still waiting happened before this input: it keeps its
		// place ahead of it instead of being replaced later.
		if o.move != nil {
			o.queue = append(o.queue, *o.move)
			o.move = nil
		}
		o.queue = append(o.queue, it)
	}
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// next returns the item to send now, or nil and how long to wait before
// asking again (0: until woken).
func (o *outbox) next() (*outItem, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// Queued input is older than the waiting move, if any (see put).
	if len(o.queue) > 0 {
		it := o.queue[0]
		o.queue = o.queue[1:]
		return &it, 0
	}
	if o.move == nil {
		return nil, 0
	}
	// The move waits while the network is behind.
	if o.move.conn != nil && o.move.conn.Unsent() > moveBacklog {
		return nil, moveRecheck
	}
	it := o.move
	o.move = nil
	return it, 0
}

// run sends until stop closes.
func (o *outbox) run(stop <-chan struct{}) {
	var timer *time.Timer
	for {
		it, wait := o.next()
		if it != nil {
			send := it.send
			if send == nil {
				if it.conn == nil {
					continue
				}
				send = it.conn.SendPacket
			}
			if err := send(it.pkt); err != nil {
				slog.Debug("send input failed", "type", it.pkt.Type, "err", err)
			}
			continue
		}
		var retry <-chan time.Time
		if wait > 0 {
			if timer == nil {
				timer = time.NewTimer(wait)
			} else {
				timer.Reset(wait)
			}
			retry = timer.C
		}
		select {
		case <-stop:
			return
		case <-o.wake:
		case <-retry:
		}
		if retry != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}
