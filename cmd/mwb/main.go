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
	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/network"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debug := flag.Bool("debug", false, "enable debug logging")
	edgeSide := flag.String("edge", "", "screen edge to switch: left or right (overrides config)")
	bidirectional := flag.Bool("bidi", false, "enable bidirectional input (send local input to remote)")
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

	slog.Debug("debug logging enabled")
	slog.Info("mwb starting", "host", cfg.Host, "port", cfg.MessagePort(), "name", cfg.Name, "bidirectional", *bidirectional, "edge", *edgeSide)

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
		Mouse:    mouse,
		Keyboard: keyboard,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start TCP server to accept incoming connections from Windows MWB
	serverStop := make(chan struct{})
	incomingCh := network.ListenAndAccept(cfg.MessagePort(), cfg.Key, cfg.Name, serverStop)
	defer close(serverStop)

	go func() {
		for {
			// Race: try outbound connect AND accept inbound — first one wins
			addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.MessagePort())
			slog.Info("connecting", "addr", addr)

			connCh := make(chan *network.Conn, 1)
			go func() {
				c, err := network.Connect(addr, cfg.Key, cfg.Name, 10*time.Second)
				if err != nil {
					slog.Debug("outbound connect failed", "err", err)
					return
				}
				// Non-blocking send: if inbound already won the race, close this conn
				select {
				case connCh <- c:
				default:
					_ = c.Close()
				}
			}()

			// Wait for either outbound or inbound connection
			var conn *network.Conn
			select {
			case conn = <-connCh:
				slog.Info("connected (outbound)", "remote", conn.RemoteName)
			case conn = <-incomingCh:
				slog.Info("connected (inbound)", "remote", conn.RemoteName)
			}

			// Start clipboard sharing — use the auto-detected display
			clipMgr := clipboard.NewManager(conn, capture.DetectDisplay())
			handler.Clipboard = clipMgr
			clipMgr.Start()

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

				// When we receive MachineSwitched, mark ourselves as active
				handler.OnActivated = func() {
					cap.SetActive(true)
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

			clipMgr.Stop() // waits for goroutine via WaitGroup

			_ = conn.Close()
			slog.Info("disconnected, will reconnect in 100ms")
		}
	}()

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
}
