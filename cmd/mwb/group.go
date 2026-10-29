// cmd/mwb/group.go
package main

import (
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/lucky-verma/mwb-linux/internal/capture"
	"github.com/lucky-verma/mwb-linux/internal/clipboard"
	"github.com/lucky-verma/mwb-linux/internal/config"
	"github.com/lucky-verma/mwb-linux/internal/group"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/status"
)

// groupSetup is what runGroup needs from main.
type groupSetup struct {
	cfg            *config.Config
	configPath     string
	saveLayout     bool // false when the layout must not be written back
	incoming       <-chan *network.Conn
	mouse          network.MouseDevice
	keyboard       network.KeyboardDevice
	keyboardLayout string
	sharing        bool // forward this machine's input (bidirectional)
	clipboard      bool
	status         *status.Writer
	// onClipboard publishes the clipboard manager for inbound file channels.
	onClipboard func(*clipboard.Manager)
	// activeID is a member's control connection ID, which an inbound file
	// channel's header must echo.
	activeID *atomic.Uint32
}

// capturerSlot holds the running capturer, which is replaced when the
// layout moves this machine's neighbours to other sides. It forwards the
// group's cursor announcements to whichever capturer is current.
type capturerSlot struct {
	mu  sync.Mutex
	cap *capture.Capturer
}

func (s *capturerSlot) get() *capture.Capturer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cap
}

func (s *capturerSlot) Arrive(side string, y int32) {
	if c := s.get(); c != nil {
		c.Arrive(side, y)
	}
}

func (s *capturerSlot) Follow(t group.Target, side string, y int32) {
	if c := s.get(); c != nil {
		c.Follow(t, side, y)
	}
}

func (s *capturerSlot) Lost(name string) {
	if c := s.get(); c != nil {
		c.Lost(name)
	}
}

// runGroup runs this machine as one screen in a group of mwb-linux machines,
// which a pair of them also is. It returns when stop is closed.
func runGroup(gs groupSetup, stop <-chan struct{}) {
	cfg := gs.cfg
	screen := func() (int32, int32) {
		capture.DetectDisplay() // xrandr needs DISPLAY, which detection sets
		if s, ok := capture.DetectScreenSize(); ok {
			return s.Width, s.Height
		}
		return 0, 0
	}
	legacyEdge := ""
	if len(cfg.Layout) == 0 {
		legacyEdge = cfg.Edge // a pair set up before layouts: keep its side
	}

	slot := &capturerSlot{}
	var g *group.Group
	var clipMgr *clipboard.Manager
	opts := group.Options{
		Name:       cfg.Name,
		Key:        cfg.Key,
		Port:       cfg.MessagePort(),
		Bootstrap:  cfg.Host,
		Layout:     group.Layout{Order: cfg.Layout, Pos: group.ParseGrid(cfg.Grid), Changed: cfg.LayoutChanged},
		LegacyEdge: legacyEdge,
		StorePath:  group.StorePath(),
		Screen:     screen,
		Sharing:    gs.sharing,
		Session: func(name string, conn *network.Conn) (*network.Handler, func()) {
			held := network.NewHeldInput(gs.mouse, gs.keyboard)
			m, k := held.Devices()
			h := &network.Handler{
				Mouse:             m,
				Keyboard:          k,
				InboundMultiplier: cfg.InboundMultiplier,
				KeyboardLayout:    gs.keyboardLayout,
			}
			if clipMgr != nil {
				h.Clipboard = clipMgr
			}
			gs.activeID.Store(conn.MachineID)
			return h, func() {
				// Keys and buttons a member was holding when its link ended
				// would otherwise stay down here.
				held.Release()
				gs.activeID.CompareAndSwap(conn.MachineID, 0)
			}
		},
		Changed: func() {
			if g == nil {
				return
			}
			if err := gs.status.Group(g.Snapshot()); err != nil {
				slog.Debug("status file not updated", "err", err)
			}
		},
	}
	if gs.saveLayout {
		opts.SaveLayout = func(l group.Layout) error {
			return config.SaveLayout(gs.configPath, l.Order, group.FormatGrid(l.Pos), l.Changed)
		}
	}
	if gs.sharing {
		opts.Cursor = slot
	}

	var restartMu sync.Mutex
	startCapture := func() {
		screenInfo := capture.GetScreenSizeXrandr()
		c := capture.New(nil, screenInfo, "")
		c.SetAccelMultiplier(cfg.AccelMultiplier)
		c.SetNavigator(g)
		if err := c.Run(); err != nil {
			slog.Error("capture start failed; this machine's keyboard and mouse stay local", "err", err)
			return
		}
		slot.mu.Lock()
		slot.cap = c
		slot.mu.Unlock()
		slog.Info("sharing this machine's keyboard and mouse with the group")
	}
	opts.NeighborsChanged = func() {
		if !gs.sharing {
			return
		}
		// The edges to watch changed: start over with the new ones.
		restartMu.Lock()
		defer restartMu.Unlock()
		slot.mu.Lock()
		old := slot.cap
		slot.cap = nil
		slot.mu.Unlock()
		if old == nil {
			return // not started yet; it will start with the new sides
		}
		wasRemote := old.RemoteAt() != ""
		old.Stop()
		startCapture()
		if wasRemote {
			g.Moved("", "", 32767) // input is local again: say so
		}
	}

	g = group.New(opts)
	g.Start()

	if gs.clipboard {
		clipMgr = clipboard.NewGroupManager(g.Conns, capture.DetectDisplay())
		if cfg.FileTransferEnabled() {
			clipMgr.OnFileCopy = func(paths []string) {
				// A copied file goes to the machine the pointer is on, if that
				// is where it is; otherwise to every machine online. Clipboard
				// text and images are small and still broadcast (inside the
				// clipboard manager); only the larger file transfer is steered,
				// so it does not travel to every machine at once.
				targets := g.FileCopyTargets()
				slog.Info("sending copied file(s)", "files", len(paths), "to", len(targets))
				for _, conn := range targets {
					sender := network.FileSender{
						Addr:        net.JoinHostPort(conn.RemoteHost(), strconv.Itoa(cfg.ClipboardPort())),
						SecurityKey: cfg.Key,
						MachineName: cfg.Name,
						MachineID:   conn.MachineID,
						MaxSize:     cfg.MaxFileSize,
						DialTimeout: fileDialTimeout,
					}
					if err := sender.Send(paths); err != nil {
						slog.Error("outbound file copy failed", "remote", conn.RemoteName, "err", err)
					}
				}
			}
		}
		gs.onClipboard(clipMgr)
		clipMgr.Start()
	}
	if gs.sharing {
		startCapture()
	}
	if err := gs.status.LocalSharing(gs.sharing && slot.get() != nil); err != nil {
		slog.Debug("status file not updated", "err", err)
	}
	opts.Changed()

	g.Run(gs.incoming, stop)

	if c := slot.get(); c != nil {
		c.Stop()
	}
	if clipMgr != nil {
		clipMgr.Stop()
	}
}
