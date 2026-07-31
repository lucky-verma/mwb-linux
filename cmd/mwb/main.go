// cmd/mwb/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/capture"
	"github.com/lucky-verma/mwb-linux/internal/clipboard"
	"github.com/lucky-verma/mwb-linux/internal/config"
	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/selfupdate"
)

// version is stamped at release time via -ldflags "-X main.version=...".
// Builds made outside the release pipeline report "dev".
var version = "dev"

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

	case "update":
		fs := flag.NewFlagSet("update", flag.ExitOnError)
		checkOnly := fs.Bool("check", false, "report whether an update exists without installing it")
		force := fs.Bool("force", false, "reinstall even when already up to date")
		_ = fs.Parse(os.Args[2:])

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		err := selfupdate.Run(ctx, selfupdate.Options{
			CurrentVersion: version,
			CheckOnly:      *checkOnly,
			Force:          *force,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "update failed:", err)
			os.Exit(1)
		}
		return true
	}
	return false
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
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nCreate config at %s with:\n\n", *configPath)
		fmt.Fprintf(os.Stderr, "  host = \"192.168.1.100\"\n  key = \"YourSecurityKey\"\n  name = \"linux\"\n\n")
		os.Exit(1)
	}

	// Apply config defaults for flags not explicitly set on the command line.
	// This allows config.toml to set edge/remote dims without requiring CLI flags.
	if *edgeSide == "" {
		*edgeSide = cfg.Edge
	}
	if *edgeSide == "" {
		*edgeSide = "right" // final fallback
	}

	// Clipboard runs by default. Either config (clipboard = false) or the
	// --no-clipboard flag disables it; the flag wins over config.
	clipboardEnabled := cfg.ClipboardEnabled() && !*noClipboard
	keyboardLayout := input.ResolveKeyboardLayout(cfg.KeyboardLayout)

	slog.Debug("debug logging enabled")
	slog.Info("mwb starting", "host", cfg.Host, "port", cfg.MessagePort(), "name", cfg.Name, "bidirectional", *bidirectional, "edge", *edgeSide, "clipboard", clipboardEnabled, "keyboard_layout", keyboardLayout)

	mouse, err := input.CreateVirtualMouse("mwb-mouse")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual mouse: %v\n", err)
		fmt.Fprintln(os.Stderr, "Setup required:")
		fmt.Fprintln(os.Stderr, "  1. sudo modprobe uinput")
		fmt.Fprintln(os.Stderr, "  2. echo 'uinput' | sudo tee /etc/modules-load.d/uinput.conf")
		fmt.Fprintln(os.Stderr, "  3. echo 'KERNEL==\"uinput\", GROUP=\"input\", MODE=\"0660\"' | sudo tee /etc/udev/rules.d/99-uinput.rules")
		fmt.Fprintln(os.Stderr, "  4. sudo udevadm control --reload-rules && sudo udevadm trigger /dev/uinput")
		fmt.Fprintln(os.Stderr, "  5. Ensure your user is in the 'input' group: sudo usermod -aG input $USER")
		fmt.Fprintln(os.Stderr, "Ensure your user is in the 'input' group: sudo usermod -aG input $USER")
		os.Exit(1)
	}
	defer func() { _ = mouse.Close() }()

	keyboard, err := input.CreateVirtualKeyboard("mwb-keyboard")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual keyboard: %v\n", err)
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

	// File transfer shares the control port: MWB opens a second connection there
	// for file copies. A nil handler leaves those connections rejected.
	var onFile network.InboundFile
	if cfg.FileTransferEnabled() {
		fileDir := cfg.FileDirectory()
		maxFile := cfg.MaxFileSize
		slog.Info("file transfer enabled", "dir", fileDir, "max_bytes", filetransfer.EffectiveMaxSize(maxFile))
		onFile = func(c *network.Conn, push bool) {
			res, err := filetransfer.Receive(c.Reader(), fileDir, maxFile)
			if err != nil {
				slog.Error("inbound file transfer failed", "err", err)
				return
			}
			if res.Path == "" {
				// Oversized clipboard payload rather than a file; the clipboard
				// manager owns that content, not the filesystem.
				slog.Info("received inline clipboard payload over the file channel",
					"name", res.Name, "bytes", res.Size)
				return
			}
			filetransfer.Notify(res)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start TCP server to accept incoming connections from Windows MWB.
	// Only the configured host (cfg.Host) is accepted as an inbound peer.
	serverStop := make(chan struct{})
	incomingCh, err := network.ListenAndAccept(cfg.MessagePort(), cfg.Key, cfg.Name, cfg.Host, onFile, serverStop)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error starting listener: %v\n", err)
		fmt.Fprintln(os.Stderr, "Is another mwb instance already running?")
		os.Exit(1)
	}
	defer close(serverStop)

	go func() {
		for {
			// Race: try outbound connect AND accept inbound — first one wins
			addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.MessagePort())
			slog.Info("connecting", "addr", addr)

			connectStop := make(chan struct{})
			outgoingCh := network.ConnectWithRetry(
				addr,
				cfg.Key,
				cfg.Name,
				10*time.Second,
				time.Second,
				connectStop,
			)

			// Wait for either outbound or inbound connection
			var conn *network.Conn
			select {
			case conn = <-outgoingCh:
				slog.Info("connected (outbound)", "remote", conn.RemoteName)
			case conn = <-incomingCh:
				slog.Info("connected (inbound)", "remote", conn.RemoteName)
			}
			close(connectStop)

			// Start clipboard sharing on the auto-detected display unless disabled.
			var clipMgr *clipboard.Manager
			if clipboardEnabled {
				clipMgr = clipboard.NewManager(conn, capture.DetectDisplay())
				handler.Clipboard = clipMgr
				clipMgr.Start()
			}

			// Start bidirectional capture if enabled
			var cap *capture.Capturer
			if *bidirectional {
				screen := capture.GetScreenSizeXrandr()
				slog.Info("screen detected", "width", screen.Width, "height", screen.Height)

				cap = capture.New(conn, screen, *edgeSide)
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

				// When we receive MachineSwitched, mark ourselves as active and
				// move cursor away from edge — without this the cursor stays at
				// x=0 and re-triggers the edge switch immediately on any movement.
				handler.OnActivated = func() {
					cap.SetActive(true)
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						entryX, entryY := cap.SafeEntryPosition()
						_ = exec.CommandContext(ctx, "xdotool", "mousemove", "--",
							fmt.Sprintf("%d", entryX),
							fmt.Sprintf("%d", entryY)).Run()
					}()
				}

				// When server sends NextMachine (cursor bounced off server's edge),
				// reclaim control and move cursor away from our edge
				handler.OnReclaimed = func() {
					cap.SetActive(true)
					// Move cursor to center so it doesn't immediately re-trigger edge
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer cancel()
						_ = exec.CommandContext(ctx, "xdotool", "mousemove", "--",
							fmt.Sprintf("%d", screen.Width/2),
							fmt.Sprintf("%d", screen.Height/2)).Run()
					}()
				}

				if err := cap.Run(); err != nil {
					slog.Error("capture start failed", "err", err)
				} else {
					slog.Info("bidirectional capture enabled", "edge", *edgeSide)
				}
			}

			if err := network.ReceiveLoop(conn, handler); err != nil {
				slog.Error("receive loop error", "err", err)
			}

			// Stop capture first — prevents in-flight SendPacket after conn.Close()
			if cap != nil {
				cap.Stop()
			}

			if clipMgr != nil {
				clipMgr.Stop() // waits for goroutine via WaitGroup
			}

			_ = conn.Close()
			slog.Info("disconnected, will reconnect in 100ms")
		}
	}()

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
}
