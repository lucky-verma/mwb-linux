// cmd/mwb/main.go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bketelsen/mwb/internal/capture"
	"github.com/bketelsen/mwb/internal/clipboard"
	"github.com/bketelsen/mwb/internal/config"
	"github.com/bketelsen/mwb/internal/input"
	"github.com/bketelsen/mwb/internal/network"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debug := flag.Bool("debug", false, "enable debug logging")
	edgeSide := flag.String("edge", "right", "screen edge to switch: left or right")
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

	slog.Debug("debug logging enabled")
	slog.Info("mwb starting", "host", cfg.Host, "port", cfg.MessagePort(), "name", cfg.Name, "bidirectional", *bidirectional)

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
				connCh <- c
			}()

			// Wait for either outbound or inbound connection
			var conn *network.Conn
			select {
			case conn = <-connCh:
				slog.Info("connected (outbound)", "remote", conn.RemoteName)
			case conn = <-incomingCh:
				slog.Info("connected (inbound)", "remote", conn.RemoteName)
			}

			// Start clipboard sharing
			display := os.Getenv("DISPLAY")
			if display == "" {
				display = ":1"
			}
			clipMgr := clipboard.NewManager(conn, display)
			handler.Clipboard = clipMgr
			clipMgr.Start()

			// Start bidirectional capture if enabled
			var cap *capture.Capturer
			if *bidirectional {
				screen := capture.GetScreenSizeXrandr()
				slog.Info("screen detected", "width", screen.Width, "height", screen.Height)

				cap = capture.New(conn, screen, *edgeSide)

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
						_ = exec.Command("xdotool", "mousemove", "--",
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

			clipMgr.Stop()

			if cap != nil {
				cap.Stop()
			}

			_ = conn.Close()
			slog.Info("disconnected, will reconnect in 100ms")
		}
	}()

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
}
