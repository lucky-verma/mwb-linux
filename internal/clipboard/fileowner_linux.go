//go:build linux

package clipboard

import (
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// mateFileTarget is what Caja, MATE's file manager, reads for Ctrl+V.
const mateFileTarget = "x-special/mate-copied-files"

// A copied file is offered in every format a Linux file manager reads,
// because xclip can serve only one and a guess from XDG_CURRENT_DESKTOP is
// wrong whenever that variable is missing from the service's environment, or
// names a desktop whose file manager wants another format:
//
//	x-special/gnome-copied-files  Nautilus, Nemo
//	x-special/mate-copied-files   Caja
//	text/uri-list                 Thunar, Dolphin, PCManFM, most toolkits
//	UTF8_STRING, text/plain       the path, for anything that pastes text
func fileTargets(path string) map[string][]byte {
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	// Nautilus rejects a trailing newline: it becomes an empty URI entry.
	special := []byte("copy\n" + uri)
	return map[string][]byte{
		gnomeFileTarget:            special,
		mateFileTarget:             special,
		uriListTarget:              []byte(uri + "\r\n"),
		"UTF8_STRING":              []byte(path),
		"text/plain;charset=utf-8": []byte(path),
		"text/plain":               []byte(path),
		"STRING":                   []byte(path),
	}
}

// fileOwner owns the X CLIPBOARD selection while it offers one file, and
// answers every request for it until another program takes the selection.
type fileOwner struct {
	conn *xgb.Conn
	win  xproto.Window
	done chan struct{}
}

var (
	ownerMu      sync.Mutex
	currentOwner *fileOwner
	// ourSelectionWindows holds the X windows this process uses to own the
	// CLIPBOARD selection while it offers a received file. The XFIXES watcher
	// consults it so mwb's own staging is never mistaken for the user copying
	// something (which would echo the file straight back to the sender).
	ourSelectionWindows = map[xproto.Window]bool{}
)

func markOurSelectionWindow(win xproto.Window) {
	ownerMu.Lock()
	ourSelectionWindows[win] = true
	ownerMu.Unlock()
}

func unmarkOurSelectionWindow(win xproto.Window) {
	ownerMu.Lock()
	delete(ourSelectionWindows, win)
	ownerMu.Unlock()
}

// isOurSelectionWindow reports whether win is one this process owns the
// clipboard with. Safe to call with win == 0 (never ours).
func isOurSelectionWindow(win xproto.Window) bool {
	ownerMu.Lock()
	defer ownerMu.Unlock()
	return ourSelectionWindows[win]
}

// offerFile puts path on the clipboard in every file format, replacing any
// file this process offered before.
func offerFile(display, path string) error {
	o, err := newFileOwner(display, path)
	if err != nil {
		return err
	}
	ownerMu.Lock()
	old := currentOwner
	currentOwner = o
	ownerMu.Unlock()
	if old != nil {
		old.close()
	}
	return nil
}

func newFileOwner(display, path string) (*fileOwner, error) {
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
	root := setup.Roots[conn.DefaultScreen].Root

	win, err := xproto.NewWindowId(conn)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateWindowChecked(conn, 0, win, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, 0, nil).Check(); err != nil {
		return nil, err
	}
	// Register the window before taking the selection, so the XFIXES watcher
	// already knows the imminent ownership change is ours, not a user copy.
	markOurSelectionWindow(win)
	defer func() {
		if !ok {
			unmarkOurSelectionWindow(win)
		}
	}()

	atom := func(name string) (xproto.Atom, error) {
		r, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			return 0, err
		}
		return r.Atom, nil
	}
	clipboard, err := atom("CLIPBOARD")
	if err != nil {
		return nil, err
	}
	targetsAtom, err := atom("TARGETS")
	if err != nil {
		return nil, err
	}
	timestampAtom, err := atom("TIMESTAMP")
	if err != nil {
		return nil, err
	}
	data := map[xproto.Atom][]byte{}
	names := map[xproto.Atom]string{}
	offered := []xproto.Atom{targetsAtom, timestampAtom}
	for name, payload := range fileTargets(path) {
		a, err := atom(name)
		if err != nil {
			return nil, err
		}
		data[a], names[a] = payload, name
		offered = append(offered, a)
	}

	if err := xproto.SetSelectionOwnerChecked(conn, win, clipboard, xproto.TimeCurrentTime).Check(); err != nil {
		return nil, err
	}
	owner, err := xproto.GetSelectionOwner(conn, clipboard).Reply()
	if err != nil {
		return nil, err
	}
	if owner.Owner != win {
		return nil, fmt.Errorf("another program kept the clipboard")
	}

	o := &fileOwner{conn: conn, win: win, done: make(chan struct{})}
	ok = true
	go o.serve(clipboard, targetsAtom, timestampAtom, offered, data, names)
	return o, nil
}

func (o *fileOwner) serve(clipboard, targetsAtom, timestampAtom xproto.Atom, offered []xproto.Atom, data map[xproto.Atom][]byte, names map[xproto.Atom]string) {
	defer close(o.done)
	defer unmarkOurSelectionWindow(o.win)
	for {
		ev, err := o.conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed
		}
		switch e := ev.(type) {
		case xproto.SelectionClearEvent:
			// Something else was copied: this file is no longer on the clipboard.
			o.conn.Close()
			return
		case xproto.SelectionRequestEvent:
			o.answer(e, clipboard, targetsAtom, timestampAtom, offered, data, names)
		}
	}
}

// answer serves one request, as ICCCM describes: write the data to the
// requestor's property, then tell it with SelectionNotify. A refusal
// reports property None.
func (o *fileOwner) answer(e xproto.SelectionRequestEvent, clipboard, targetsAtom, timestampAtom xproto.Atom,
	offered []xproto.Atom, data map[xproto.Atom][]byte, names map[xproto.Atom]string) {
	prop := e.Property
	if prop == xproto.AtomNone {
		prop = e.Target // an obsolete requestor
	}
	reply := prop
	switch {
	case e.Selection != clipboard:
		reply = xproto.AtomNone
	case e.Target == targetsAtom:
		buf := make([]byte, 4*len(offered))
		for i, a := range offered {
			xgb.Put32(buf[i*4:], uint32(a))
		}
		xproto.ChangeProperty(o.conn, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomAtom, 32, uint32(len(offered)), buf)
	case e.Target == timestampAtom:
		buf := make([]byte, 4)
		xgb.Put32(buf, uint32(xproto.TimeCurrentTime))
		xproto.ChangeProperty(o.conn, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomInteger, 32, 1, buf)
	default:
		payload, ok := data[e.Target]
		if !ok {
			reply = xproto.AtomNone
			break
		}
		slog.Debug("file clipboard requested", "target", names[e.Target])
		xproto.ChangeProperty(o.conn, xproto.PropModeReplace, e.Requestor, prop, e.Target, 8, uint32(len(payload)), payload)
	}
	notify := xproto.SelectionNotifyEvent{
		Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: reply,
	}
	xproto.SendEvent(o.conn, false, e.Requestor, xproto.EventMaskNoEvent, string(notify.Bytes()))
}

func (o *fileOwner) close() {
	o.conn.Close()
	<-o.done
}
