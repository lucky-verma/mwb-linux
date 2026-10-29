// internal/config/config.go
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Host         string `toml:"host"`
	Key          string `toml:"key"`
	Name         string `toml:"name"`
	Port         int    `toml:"port"`
	RemoteWidth  int    `toml:"remote_width"`
	RemoteHeight int    `toml:"remote_height"`
	Edge         string `toml:"edge"`
	// EdgeChanged is when edge was last chosen, in Unix seconds; 0 = unknown.
	// mwb-gui sets it, and mwb sets it when it adopts a Linux peer's choice.
	EdgeChanged int64 `toml:"edge_changed"`
	Clipboard   *bool `toml:"clipboard"` // nil = unset, treated as enabled

	// Layout is the left-to-right order of the screens in a group of
	// mwb-linux machines, by machine name; it replaces edge for peer =
	// "linux". mwb keeps it in step across the group: the most recent change
	// wins, and LayoutChanged (Unix seconds, 0 = unknown) is when it was made.
	Layout        []string `toml:"layout"`
	LayoutChanged int64    `toml:"layout_changed"`
	// Grid is the two-dimensional arrangement, each entry "name:col:row". Empty
	// means the arrangement is the single row in Layout. Layout is always kept
	// as the row projection, so an older build still reads a sensible order.
	Grid []string `toml:"grid"`

	// Interface names the network interface mwb uses, such as "wlp4s0":
	// it listens only on that interface's address and connects out from it.
	// Empty means every interface. A name rather than an address, so a new
	// DHCP lease does not break it.
	Interface string `toml:"interface"`

	// FileTransfer controls the MWB file copy channel. nil = enabled.
	FileTransfer *bool `toml:"file_transfer"`

	// LegacyCrypto selects the stream encryption PowerToys used before 0.101: a
	// PBKDF2 salt and AES IV both fixed at build time, at 50,000 iterations.
	// Current PowerToys sends a random salt and IV in the clear ahead of the
	// ciphertext and derives at 100,000 iterations. nil = current scheme; set
	// this only when the Windows peer is pinned to an older PowerToys.
	LegacyCrypto *bool `toml:"legacy_crypto"`

	// FileDir is retained only so older configs continue to decode. Clipboard
	// files now use private cache staging and appear in a chosen folder on paste;
	// writing every copied file into a visible directory was incorrect MWB UX.
	// Deprecated: ignored.
	FileDir string `toml:"file_dir"`

	// MaxFileSize caps a single transfer in bytes. 0 = MWB's own 100 MB limit,
	// which a stock Windows peer neither exceeds nor accepts.
	MaxFileSize int64 `toml:"max_file_size"`

	// KeyboardLayout controls inbound Windows->Linux keyboard mapping. "auto"
	// detects the local Linux layout when possible; unsupported layouts fall back
	// to the US-compatible mapping.
	KeyboardLayout string `toml:"keyboard_layout"`

	// AccelMultiplier scales raw evdev deltas before they move the remote cursor
	// (outbound, Linux->Windows). The Windows side adds no acceleration of its
	// own, so this is the only outbound speed knob. <= 0 means unset.
	AccelMultiplier float64 `toml:"accel_multiplier"`

	// InboundMultiplier scales Windows->Linux cursor movement (the inbound,
	// absolute-mirror direction). 1.0 mirrors Windows 1:1; raise it for a faster
	// local cursor when Windows is in control. <= 0 means unset (defaults to 1).
	InboundMultiplier float64 `toml:"inbound_multiplier"`

	// Peer names what runs on the other machine. "windows" (the default) is
	// PowerToys Mouse Without Borders and keeps the original behavior exactly.
	// "linux" is another mwb-linux instance: it enables deterministic
	// connection pairing and explicit cursor-ownership packets, which PowerToys
	// otherwise provides itself. Only set it when both ends run mwb-linux.
	Peer string `toml:"peer"`

	// Bidirectional sends local input to the peer when the cursor crosses the
	// configured edge, like the -bidi flag. nil = off for a PowerToys peer, so
	// the installed service stays receive-only unless asked, and on for a
	// Linux peer, because two receive-only machines can never move the cursor
	// at all. An explicit -bidi flag overrides it.
	Bidirectional *bool `toml:"bidirectional"`
}

// Peer types accepted by the peer setting.
const (
	PeerWindows = "windows"
	PeerLinux   = "linux"
)

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("config: key is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 15100
	}
	if cfg.Name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "linux"
		}
		cfg.Name = hostname
	}
	if len(cfg.Name) > 15 {
		cfg.Name = cfg.Name[:15]
	}
	if cfg.RemoteWidth == 0 {
		cfg.RemoteWidth = 1920
	}
	if cfg.RemoteHeight == 0 {
		cfg.RemoteHeight = 1080
	}
	if cfg.AccelMultiplier <= 0 {
		cfg.AccelMultiplier = 2.0
	}
	if cfg.InboundMultiplier <= 0 {
		cfg.InboundMultiplier = 1.0
	}
	if cfg.KeyboardLayout == "" {
		cfg.KeyboardLayout = "auto"
	}
	if cfg.Edge == "" {
		cfg.Edge = "left"
	}
	switch cfg.Peer {
	case "":
		cfg.Peer = PeerWindows
	case PeerWindows, PeerLinux:
	default:
		// Fail closed: a typo must not silently pick a pairing strategy.
		return nil, fmt.Errorf("config: peer must be %q or %q, got %q", PeerWindows, PeerLinux, cfg.Peer)
	}
	// A Linux peer may leave host empty and wait for the other machine to
	// connect (see WaitsForPeer). A PowerToys peer always needs it, as before.
	if cfg.Host == "" && cfg.Peer != PeerLinux {
		return nil, fmt.Errorf("config: host is required")
	}

	// The config holds the plaintext security key — the only secret protecting
	// input injection. Tighten permissions to owner-only (0600) so other local
	// accounts can't read it. This self-heals older installs whose file was
	// created world-readable (0644) without requiring a manual chmod.
	if err := secureConfigPermissions(path); err != nil {
		slog.Warn("could not tighten config file permissions", "path", path, "err", err)
	}
	if len(cfg.Key) < 12 {
		slog.Warn("security key is short; use a long, random key — it is the only secret protecting keyboard/mouse injection",
			"length", len(cfg.Key))
	}

	return &cfg, nil
}

// secureConfigPermissions restricts the config file to 0600 (owner read/write)
// if it is currently group- or world-accessible.
func secureConfigPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil // already 0600 or tighter
	}
	return os.Chmod(path, 0o600)
}

func (c *Config) MessagePort() int {
	return c.Port + 1
}

// ClipboardPort is where file and clipboard channels are opened. MWB runs two
// listeners and they are not the same one:
//
//	skMessageServer   = new TcpServer(TcpPort + 1, TCPServerThread);
//	skClipboardServer = new TcpServer(TcpPort,     AcceptConnectionAndSendClipboardData);
//
// Control is TcpPort+1, which MessagePort returns. Clipboard is the base port,
// and ConnectToRemoteClipboardSocket dials it. Sending a file channel to the
// control port instead reaches TCPServerThread, which answers with a control
// Handshake and never routes the connection to the file receiver.
func (c *Config) ClipboardPort() int {
	return c.Port
}

// ClipboardEnabled reports whether clipboard sharing should run. An absent
// clipboard key keeps it enabled, preserving the prior default behavior.
func (c *Config) ClipboardEnabled() bool {
	return c.Clipboard == nil || *c.Clipboard
}

// FileTransferEnabled reports whether the file copy channel should run.
// An absent setting is treated as enabled, matching clipboard.
func (c *Config) FileTransferEnabled() bool {
	return c.FileTransfer == nil || *c.FileTransfer
}

// LegacyCryptoEnabled reports whether to speak the pre-0.101 PowerToys stream
// encryption. An absent setting means the current scheme, which is what a
// stock, up-to-date Windows peer speaks.
func (c *Config) LegacyCryptoEnabled() bool {
	return c.LegacyCrypto != nil && *c.LegacyCrypto
}

// LinuxPeer reports whether the other machine runs mwb-linux rather than
// PowerToys.
func (c *Config) LinuxPeer() bool {
	return c.Peer == PeerLinux
}

// BidirectionalEnabled reports whether config asks for bidirectional input.
// An absent setting means receive-only for a PowerToys peer, preserving the
// prior default, and two-way for a Linux peer, as PowerToys pairs behave.
func (c *Config) BidirectionalEnabled() bool {
	if c.Bidirectional == nil {
		return c.LinuxPeer()
	}
	return *c.Bidirectional
}

// WaitsForPeer reports whether this machine only accepts a connection from
// the other one instead of dialing it: a Linux peer with no host set. The
// other machine dials this one's address, as when pairing PowerToys machines.
func (c *Config) WaitsForPeer() bool {
	return c.LinuxPeer() && c.Host == ""
}

// FileDirectory resolves the legacy direct-download directory.
// Deprecated: clipboard file transfers no longer use this path.
func (c *Config) FileDirectory() string {
	if c.FileDir != "" {
		return os.ExpandEnv(c.FileDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "mwb")
	}
	return filepath.Join(home, "Downloads", "mwb")
}
