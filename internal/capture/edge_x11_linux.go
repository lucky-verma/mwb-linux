//go:build linux

package capture

import (
	"fmt"
	"os"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// edgeSource reports when the pointer touches the shared screen edge.
type edgeSource interface {
	Hits() <-chan int32 // coordinate along the edge of each touch (Y for a
	// left/right edge, X for a top/bottom edge)
	Raise()
	Close()
}

// isWaylandSession reports whether the desktop is a Wayland session, where
// XWayland only learns the pointer position while it is over an X11 window.
// QueryPointer on the root window then returns a frozen position.
func isWaylandSession() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}

// edgeStrip is a transparent, one-pixel, override-redirect XWayland window
// along the shared edge. The compositor delivers pointer events to it whenever
// the cursor reaches that edge, so XWayland reports a MotionNotify there even
// though it cannot see the cursor anywhere else on a Wayland desktop.
// XWayland does not send LeaveNotify when the cursor moves onto a Wayland
// surface, so only arrival is reported; re-arming is done from evdev motion.
type edgeStrip struct {
	conn    *xgb.Conn
	win     xproto.Window
	hits    chan int32
	reportX bool          // report RootX (top/bottom edge) rather than RootY
	done    chan struct{} // closed when readEvents returns
}

func newEdgeStrip(display, side string) (*edgeStrip, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()

	setup := xproto.Setup(conn)
	if conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		return nil, fmt.Errorf("default screen %d is out of range", conn.DefaultScreen)
	}
	scr := setup.Roots[conn.DefaultScreen]

	// A 32-bit visual with a zero background keeps the strip invisible. Without
	// any background the compositor never maps the surface and no events arrive.
	var visual xproto.Visualid
	for _, d := range scr.AllowedDepths {
		if d.Depth == 32 && len(d.Visuals) > 0 {
			visual = d.Visuals[0].VisualId
			break
		}
	}
	if visual == 0 {
		return nil, fmt.Errorf("no 32-bit visual for a transparent edge window")
	}
	cmap, err := xproto.NewColormapId(conn)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateColormapChecked(conn, xproto.ColormapAllocNone, cmap, scr.Root, visual).Check(); err != nil {
		return nil, fmt.Errorf("create colormap: %w", err)
	}

	// A left/right edge is a one-pixel-wide strip the full height of the screen;
	// a top/bottom edge is a one-pixel-tall strip the full width. The strip
	// reports the coordinate along itself: Y for left/right, X for top/bottom.
	var x, y int16
	var w, h uint16
	reportX := false
	switch side {
	case "left":
		x, y, w, h = 0, 0, 1, scr.HeightInPixels
	case "right":
		x, y, w, h = int16(scr.WidthInPixels)-1, 0, 1, scr.HeightInPixels
	case "up":
		x, y, w, h, reportX = 0, 0, scr.WidthInPixels, 1, true
	case "down":
		x, y, w, h, reportX = 0, int16(scr.HeightInPixels)-1, scr.WidthInPixels, 1, true
	default:
		return nil, fmt.Errorf("unknown edge side %q", side)
	}
	win, err := xproto.NewWindowId(conn)
	if err != nil {
		return nil, err
	}
	err = xproto.CreateWindowChecked(conn, 32, win, scr.Root, x, y, w, h, 0,
		xproto.WindowClassInputOutput, visual,
		xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect|xproto.CwEventMask|xproto.CwColormap,
		[]uint32{0, 0, 1, xproto.EventMaskEnterWindow | xproto.EventMaskPointerMotion, uint32(cmap)}).Check()
	if err != nil {
		return nil, fmt.Errorf("create edge window: %w", err)
	}
	if err := xproto.MapWindowChecked(conn, win).Check(); err != nil {
		return nil, fmt.Errorf("map edge window: %w", err)
	}

	s := &edgeStrip{conn: conn, win: win, hits: make(chan int32, 1), reportX: reportX, done: make(chan struct{})}
	go s.readEvents()
	ok = true
	return s, nil
}

func (s *edgeStrip) readEvents() {
	defer close(s.done)
	defer close(s.hits)
	for {
		ev, xerr := s.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			return // connection closed
		}
		var along int32
		switch e := ev.(type) {
		case xproto.EnterNotifyEvent:
			if s.reportX {
				along = int32(e.RootX)
			} else {
				along = int32(e.RootY)
			}
		case xproto.MotionNotifyEvent:
			if s.reportX {
				along = int32(e.RootX)
			} else {
				along = int32(e.RootY)
			}
		default:
			continue
		}
		// Keep only the latest touch; a stale one is worthless.
		select {
		case <-s.hits:
		default:
		}
		s.hits <- along
	}
}

func (s *edgeStrip) Hits() <-chan int32 { return s.hits }

// Raise keeps the strip above windows mapped after it.
func (s *edgeStrip) Raise() {
	xproto.ConfigureWindow(s.conn, s.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
}

// Close closes the X connection, which ends readEvents, and waits for it.
func (s *edgeStrip) Close() {
	s.conn.Close()
	<-s.done
}
