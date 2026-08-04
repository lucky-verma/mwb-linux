//go:build linux

package capture

import (
	"fmt"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type pointerPositioner interface {
	Position() (x, y int32, err error)
	Close()
}

// x11Pointer keeps one X11 connection open for the lifetime of the capturer.
// QueryPointer is a single protocol round trip; no process is started per poll.
type x11Pointer struct {
	conn *xgb.Conn
	root xproto.Window
}

func newX11Pointer(display string) (*x11Pointer, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(conn)
	if conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		conn.Close()
		return nil, fmt.Errorf("default screen %d is out of range", conn.DefaultScreen)
	}
	return &x11Pointer{conn: conn, root: setup.Roots[conn.DefaultScreen].Root}, nil
}

func (p *x11Pointer) Position() (int32, int32, error) {
	reply, err := xproto.QueryPointer(p.conn, p.root).Reply()
	if err != nil {
		return -1, -1, err
	}
	return int32(reply.RootX), int32(reply.RootY), nil
}

func (p *x11Pointer) Close() {
	p.conn.Close()
}
