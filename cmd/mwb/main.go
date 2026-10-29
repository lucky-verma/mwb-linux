// cmd/mwb/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/capture"
	"github.com/lucky-verma/mwb-linux/internal/clipboard"
	"github.com/lucky-verma/mwb-linux/internal/config"
	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/selfupdate"
	"github.com/lucky-verma/mwb-linux/internal/status"
)

// version is stamped at release time via -ldflags "-X main.version=...".
// Builds made outside the release pipeline report "dev".
var version = "dev"

// fileDialTimeout bounds connection setup for an outbound file copy. It matches
// the control connection's dial timeout: the file channel reaches the same
// peer on its adjacent clipboard port, so a reachable peer answers promptly.
const fileDialTimeout = 10 * time.Second

// runSubcommand handles the verb form (`mwb update`, `mwb version`) and reports
// whether it consumed the invocation. Subcommands are dispatched before
// flag.Parse so they can own their own flag set.
func runSubcommand() bool {
	if len(os.Args) < 2 {
		return false
	}
	switch os.Args[1] {
	case "version", "--version", "-version":
		fmt.Println("mwb", version)
		return true

	case "genkey":
		// Prints only the key, so it can be captured: KEY=$(mwb genkey).
		k, err := config.GenerateKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "genkey failed:", err)
			os.Exit(1)
		}
		fmt.Println(k)
		return true

	case "update":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		checkOnly := fs.Bool("check", false, "report whether an update exists without installing it")
		force := fs.Bool("force", false, "reinstall even when already up to date")
		restart := fs.Bool("restart", false, "restart the running systemd user service after installing")
		_ = fs.Parse(os.Args[2:])

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		err := selfupdate.Run(ctx, selfupdate.Options{
			CurrentVersion: version,
			CheckOnly:      *checkOnly,
			Force:          *force,
			Restart:        *restart,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "update failed:", err)
			os.Exit(1)
		}
		return true
	}
	return false
}

// flagWasSet reports whether name was given on the command line, so config can
// supply a default without overriding an explicit flag in either direction.
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func main() {
	if runSubcommand() {
		return
	}

	configPath := flag.String("config", "", "path to config.toml")
	debug := flag.Bool("debug", false, "enable debug logging")
	edgeSide := flag.String("edge", "", "screen edge to switch: left or right (overrides config)")
	bidirectional := flag.Bool("bidi", false, "enable bidirectional input (send local input to remote)")
	noClipboard := flag.Bool("no-clipboard", false, "disable clipboard sharing (overrides config)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *configPath == "" {
		home, _ := os.UserHomeDir()
		*configPath = filepath.Join(home, ".config", "mwb", "config.toml")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Without usable settings, wait for them rather than exit: an exit
		// makes systemd restart mwb every few seconds, forever, on a fresh
		// install. Saving the settings starts it.
		cfg = waitForConfig(*configPath, err)
	}

	// Apply config defaults for flags not explicitly set on the command line.
	// This allows config.toml to set edge/remote dims without requiring CLI flags.
	if *edgeSide == "" {
		*edgeSide = cfg.Edge
	}
	if *edgeSide == "" {
		*edgeSide = "right" // final fallback
	}
	// bidirectional = true in config enables it for the service; an explicit
	// -bidi or -bidi=false on the command line still wins.
	if !flagWasSet("bidi") {
		*bidirectional = cfg.BidirectionalEnabled()
	}
	linuxPeer := cfg.LinuxPeer()

	// Clipboard runs by default. Either config (clipboard = false) or the
	// --no-clipboard flag disables it; the flag wins over config.
	clipboardEnabled := cfg.ClipboardEnabled() && !*noClipboard
	keyboardLayout := input.ResolveKeyboardLayout(cfg.KeyboardLayout)
	if linuxPeer {
		// A Linux sender encodes physical key positions through the US table,
		// so decoding with a Windows layout profile would move keys (Y and Z
		// on a German keyboard, for instance). Decode positionally and let
		// this desktop's own layout produce the character.
		switch input.CanonicalKeyboardLayout(cfg.KeyboardLayout) {
		case "auto", "":
			keyboardLayout = "us"
		case "us":
		default:
			slog.Warn("peer = linux sends physical key positions; keyboard_layout other than auto/us may type the wrong characters",
				"keyboard_layout", keyboardLayout)
		}
	}

	if cfg.Interface != "" {
		st := status.NewWriter(status.Path(), status.Status{PID: os.Getpid(), Version: version, Interface: cfg.Interface})
		network.SetBindAddress(waitForInterface(cfg.Interface, st))
	}

	// PowerToys 0.101 changed the stream encryption. Pick the scheme before any
	// connection is attempted.
	network.SetLegacyCrypto(cfg.LegacyCryptoEnabled())
	if cfg.LegacyCryptoEnabled() {
		slog.Warn("legacy_crypto is set: using the stream encryption PowerToys used before 0.101")
	}

	slog.Debug("debug logging enabled")
	slog.Info("mwb starting", "host", cfg.Host, "port", cfg.MessagePort(), "name", cfg.Name, "peer", cfg.Peer, "bidirectional", *bidirectional, "edge", *edgeSide, "clipboard", clipboardEnabled, "keyboard_layout", keyboardLayout)

	mouse, err := input.CreateVirtualMouse("mwb-mouse")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual mouse: %v\n", err)
		fmt.Fprintln(os.Stderr, uinputHelp(currentUinputState(err)))
		os.Exit(1)
	}
	defer func() { _ = mouse.Close() }()

	keyboard, err := input.CreateVirtualKeyboard("mwb-keyboard")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual keyboard: %v\n", err)
		fmt.Fprintln(os.Stderr, uinputHelp(currentUinputState(err)))
		os.Exit(1)
	}
	defer func() { _ = keyboard.Close() }()

	slog.Info("virtual input devices created")

	handler := &network.Handler{
		Mouse:             mouse,
		Keyboard:          keyboard,
		InboundMultiplier: cfg.InboundMultiplier,
		KeyboardLayout:    keyboardLayout,
	}

	// File transfer runs on its own connection. A nil handler leaves inbound
	// file connections rejected and stops the clipboard from offering to send.
	var activeClip atomic.Pointer[clipboard.Manager]
	var activeMachineID atomic.Uint32
	var onFile network.InboundFile
	if cfg.FileTransferEnabled() {
		maxFile := cfg.MaxFileSize
		slog.Info("file transfer enabled", "mode", "clipboard staging", "max_bytes", filetransfer.EffectiveMaxSize(maxFile))
		onFile = func(c *network.Conn, push bool) {
			if !push {
				slog.Warn("peer opened a pull file channel, but no outbound clipboard was requested")
				return
			}
			clipMgr := activeClip.Load()
			if clipMgr == nil {
				slog.Warn("rejecting inbound clipboard payload: clipboard manager unavailable")
				return
			}
			_, err := clipMgr.HandleFileChannel(c.Reader(), maxFile)
			if err != nil {
				slog.Error("inbound file transfer failed", "err", err)
			}
		}
	}

	// Connection state for mwb-gui; a no-op when XDG_RUNTIME_DIR is unset.
	statusFile := status.NewWriter(status.Path(), status.Status{
		Peer:          cfg.Peer,
		Host:          cfg.Host,
		Waiting:       cfg.WaitsForPeer(),
		Addresses:     localAddresses(),
		Bidirectional: *bidirectional,
		Edge:          *edgeSide,
		Clipboard:     clipboardEnabled,
		Interface:     cfg.Interface,
		PID:           os.Getpid(),
		Version:       version,
	})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start TCP server to accept incoming connections from Windows MWB.
	// Only the configured host (cfg.Host) is accepted as an inbound peer. A
	// Linux peer waiting for the other machine has no host, so it accepts a
	// key-authenticated connection from the local network instead.
	waiting := cfg.WaitsForPeer()
	allowedHost := cfg.Host
	if linuxPeer {
		// A group member's address may name its port; inbound checks the host.
		if h, _, err := net.SplitHostPort(cfg.Host); err == nil {
			allowedHost = h
		}
	}
	if waiting {
		allowedHost = network.AcceptLocalNetwork
		slog.Info("waiting for the other computer to connect; enter one of these addresses there",
			"addresses", localAddresses(), "port", cfg.MessagePort())
	}
	serverStop := make(chan struct{})
	onKeyMismatch := func(remote string) {
		if err := statusFile.KeyMismatch(remote); err != nil {
			slog.Debug("recording key mismatch failed", "err", err)
		}
	}
	incomingCh, err := network.ListenAndAccept(cfg.MessagePort(), cfg.Key, cfg.Name, allowedHost, onFile, onKeyMismatch, serverStop)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting listener: %v\n", err)
		fmt.Fprintln(os.Stderr, "Is another mwb instance already running?")
		os.Exit(1)
	}
	defer close(serverStop)

	// Inbound file transfers arrive on the clipboard port, not the control one.
	// MWB dials skClipboardServer for them, so a client that only listens on
	// the control port is never offered a file.
	if onFile != nil {
		if err := network.ListenFileChannel(cfg.ClipboardPort(), cfg.Key, cfg.Name, allowedHost, activeMachineID.Load, onFile, serverStop); err != nil {
			slog.Error("inbound file transfers disabled", "port", cfg.ClipboardPort(), "err", err)
		}
	}

	if linuxPeer {
		// Linux machines form a group, of two or up to six.
		go runGroup(groupSetup{
			cfg:            cfg,
			configPath:     *configPath,
			saveLayout:     true,
			incoming:       incomingCh,
			mouse:          mouse,
			keyboard:       keyboard,
			keyboardLayout: keyboardLayout,
			sharing:        *bidirectional,
			clipboard:      clipboardEnabled,
			status:         statusFile,
			onClipboard:    func(m *clipboard.Manager) { activeClip.Store(m) },
			activeID:       &activeMachineID,
		}, serverStop)
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig)
		statusFile.Remove()
		return
	}

	go func() {
		for {
			// Race: try outbound connect AND accept inbound — first one wins
			if err := statusFile.Connecting(); err != nil {
				slog.Debug("status file not updated", "err", err)
			}

			connectStop := make(chan struct{})
			var outgoingCh <-chan *network.Conn
			if !waiting {
				addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.MessagePort()))
				slog.Info("connecting", "addr", addr)
				outgoingCh = network.ConnectWithRetry(
					addr,
					cfg.Key,
					cfg.Name,
					10*time.Second,
					time.Second,
					connectStop,
				)
			}

			// Wait for either outbound or inbound connection
			var conn *network.Conn
			var outbound bool
			select {
			case conn = <-outgoingCh:
				outbound = true
				slog.Info("connected (outbound)", "remote", conn.RemoteName)
			case conn = <-incomingCh:
				slog.Info("connected (inbound)", "remote", conn.RemoteName)
			}
			close(connectStop)
			if err := statusFile.Connected(conn.RemoteName, outbound); err != nil {
				slog.Debug("status file not updated", "err", err)
			}
			activeMachineID.Store(conn.MachineID)
			handler.OnBecameActive = nil
			handler.OnActivated = nil
			handler.OnReclaimed = nil

			// Start clipboard sharing on the auto-detected display unless disabled.
			var clipMgr *clipboard.Manager
			if clipboardEnabled {
				clipMgr = clipboard.NewManager(conn, capture.DetectDisplay())
				if cfg.FileTransferEnabled() {
					// A copied file is what the clipboard poll notices, but the
					// bytes travel over their own connection rather than the
					// clipboard packet stream.
					sender := network.FileSender{
						// The clipboard port, not addr: that one is the
						// control channel and MWB answers it with a Handshake.
						Addr:        net.JoinHostPort(peerHost(cfg.Host, conn), strconv.Itoa(cfg.ClipboardPort())),
						SecurityKey: cfg.Key,
						MachineName: cfg.Name,
						MachineID:   conn.MachineID,
						MaxSize:     cfg.MaxFileSize,
						DialTimeout: fileDialTimeout,
					}
					clipMgr.OnFileCopy = func(paths []string) {
						if err := sender.Send(paths); err != nil {
							slog.Error("outbound file copy failed", "err", err)
						}
					}
				}
				handler.Clipboard = clipMgr
				activeClip.Store(clipMgr)
				clipMgr.Start()
				// PowerToys delays large/file clipboard retrieval until the cursor
				// switches to the receiving machine. Do the same on Linux instead
				// of downloading every Windows copy immediately.
				handler.OnBecameActive = clipMgr.HandleActivation
			}

			// Start bidirectional capture if enabled
			var cap *capture.Capturer
			sharing := false // whether this machine actually forwards its input
			if *bidirectional {
				screen := capture.GetScreenSizeXrandr()
				slog.Info("screen detected", "width", screen.Width, "height", screen.Height)

				cap = capture.New(conn, screen, *edgeSide)
				if clipMgr != nil {
					// Capturer can return the cursor locally when its virtual remote
					// position reaches the shared edge; no network activation packet
					// exists on that path, so it must notify the clipboard directly.
					cap.OnActivated = clipMgr.HandleActivation
				}
				// Wire remote screen dimensions from config so virtual cursor
				// coordinate mapping is correct for non-1080p Windows displays.
				cap.SetRemoteScreen(int32(cfg.RemoteWidth), int32(cfg.RemoteHeight))
				slog.Info("remote screen configured", "width", cfg.RemoteWidth, "height", cfg.RemoteHeight)
				// Cursor speed: the only acceleration knob lives here (Windows
				// applies none of its own), so honor the configured multiplier.
				cap.SetAccelMultiplier(cfg.AccelMultiplier)
				handler.ShouldReclaim = func(requestX, _ int32, _ int32) bool {
					return cap.AcceptsReclaim(requestX)
				}
				handler.ShouldActivate = cap.AcceptsActivation

				// Recenter at the configured edge before releasing input.
				handler.OnActivated = func() { cap.SetActive(true) }
				handler.OnReclaimed = func() { cap.SetActive(true) }

				if err := cap.Run(); err != nil {
					slog.Error("capture start failed", "err", err)
				} else {
					sharing = true
					slog.Info("bidirectional capture enabled", "edge", *edgeSide)
				}
			}
			if err := statusFile.LocalSharing(sharing); err != nil {
				slog.Debug("status file not updated", "err", err)
			}
			if err := network.ReceiveLoop(conn, handler); err != nil {
				slog.Error("receive loop error", "err", err)
			}

			// Stop capture first — prevents in-flight SendPacket after conn.Close()
			if cap != nil {
				cap.Stop()
			}

			if clipMgr != nil {
				activeClip.CompareAndSwap(clipMgr, nil)
				clipMgr.Stop() // waits for goroutine via WaitGroup
			}
			activeMachineID.CompareAndSwap(conn.MachineID, 0)

			_ = conn.Close()
			slog.Info("disconnected, will reconnect in 100ms")
		}
	}()

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
	statusFile.Remove()
}
