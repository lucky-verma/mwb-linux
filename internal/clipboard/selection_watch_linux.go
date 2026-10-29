//go:build linux

package clipboard

import (
	"log/slog"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

// watchSelectionChanges raises m.clipGen once for every change of the CLIPBOARD
// selection's owner, using the X11 XFIXES extension. The X server itself
// reports the change, so this does not depend on the copying application
// serving a TIMESTAMP (which GNOME's document portal does not), and it works
// the same on Xorg and on XWayland, which is what every mainstream desktop —
// GNOME, KDE, Zorin, Parrot, XFCE — bridges its Wayland clipboard through.
//
// A fresh copy of the same file therefore counts as a change, so the file is
// re-sent; a clipboard that merely sits unchanged raises nothing, so there is
// no repeated send. Ownership changes mwb causes itself when it stages a
// received file are skipped (isOurSelectionWindow), so a staged file is never
// echoed back to the sender.
//
// If XFIXES is missing or the X connection cannot be opened, m.clipGen stays 0
// and file-copy detection falls back to comparing paths, exactly as before.
func (m *Manager) watchSelectionChanges() {
	if m.display == "" {
		return
	}
	conn, err := xgb.NewConnDisplay(m.display)
	if err != nil {
		slog.Debug("clipboard: XFIXES watch unavailable; re-copying the same file will not re-send", "err", err)
		return
	}
	// Closing the connection when Stop fires unblocks WaitForEvent below.
	go func() {
		<-m.stopCh
		conn.Close()
	}()

	if err := xfixes.Init(conn); err != nil {
		slog.Debug("clipboard: XFIXES not present; falling back to path comparison", "err", err)
		conn.Close()
		return
	}
	// XFIXES requires the client to negotiate a version before any other
	// request. SetSelectionOwnerNotify exists from version 1; ask for 5.0.
	if _, err := xfixes.QueryVersion(conn, 5, 0).Reply(); err != nil {
		slog.Debug("clipboard: XFIXES version negotiation failed", "err", err)
		conn.Close()
		return
	}

	setup := xproto.Setup(conn)
	if conn.DefaultScreen < 0 || conn.DefaultScreen >= len(setup.Roots) {
		slog.Debug("clipboard: XFIXES watch has no screen to watch")
		conn.Close()
		return
	}
	root := setup.Roots[conn.DefaultScreen].Root

	clipboard, err := xproto.InternAtom(conn, false, uint16(len("CLIPBOARD")), "CLIPBOARD").Reply()
	if err != nil {
		slog.Debug("clipboard: XFIXES watch could not resolve CLIPBOARD atom", "err", err)
		conn.Close()
		return
	}

	if err := xfixes.SelectSelectionInputChecked(conn, root, clipboard.Atom,
		xfixes.SelectionEventMaskSetSelectionOwner).Check(); err != nil {
		slog.Debug("clipboard: XFIXES could not subscribe to CLIPBOARD changes", "err", err)
		conn.Close()
		return
	}
	slog.Debug("clipboard: watching CLIPBOARD owner changes with XFIXES")

	for {
		ev, err := conn.WaitForEvent()
		if ev == nil && err == nil {
			return // connection closed by Stop
		}
		if err != nil {
			continue
		}
		sn, ok := ev.(xfixes.SelectionNotifyEvent)
		if !ok || xproto.Atom(sn.Selection) != clipboard.Atom {
			continue
		}
		// A change mwb caused itself by staging a received file is not a copy.
		if isOurSelectionWindow(sn.Owner) {
			continue
		}
		m.mu.Lock()
		m.clipGen++
		m.mu.Unlock()
	}
}
