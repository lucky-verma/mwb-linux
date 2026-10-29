# mwb-gui

A settings and status window for mwb-linux. It edits
`~/.config/mwb/config.toml` and controls the `mwb` user service. The `mwb`
client itself stays a single binary with no GUI dependencies.

```bash
sudo apt install python3-gi python3-gi-cairo gir1.2-gtk-4.0
make install-gui      # per-user, no sudo
```

Then open **Mouse Without Borders** from your apps, or run `mwb-gui`.
Use `mwb-gui --config PATH` to edit a different file.

## What it does

- **Screen arrangement.** The screens in a row, as they stand on the desk.
  With two, click to swap them; this sets `edge` (or `layout` once the
  machines are named in one). With a group of Linux computers, drag the
  screens into order; this sets `layout` and `layout_changed`, which mwb
  copies to the rest of the group. Offline computers are drawn dashed, and
  the screen with the pointer says so.
- **Connection.** Pick the other computer type (`peer`), its address
  (`host`), the shared security key (`key`, with Generate and Copy), this
  computer's name (`name`), and the other screen's size (`remote_width`,
  `remote_height`).
- **Sharing.** Choose whether this computer controls the other one
  (`bidirectional`), whether to share the clipboard (`clipboard`) and whether
  to copy files (`file_transfer`).
- **Network.** Every network interface with its address, and which one
  mwb uses (`interface`; "All interfaces" leaves it empty). The address to
  enter on the other computer follows the choice.
- **First run.** With no `config.toml` yet, the form starts as a Linux peer
  with a generated key, waiting for the other computer. mwb runs in the
  background meanwhile and starts as soon as the settings are saved.
- **Start over.** "Clear saved settings…" renames `config.toml` to
  `config.toml.bak` (owner-only, it holds the key), forgets the learned
  computers (`$XDG_STATE_HOME/mwb/members.json`), and restarts a running
  service, which then waits for new settings.
- **Background service.** Start, stop or restart the service, choose whether
  it starts at login, and read the recent log.
- **Live status.** Read from `$XDG_RUNTIME_DIR/mwb/status.json`, which the
  client writes. For a group it lists which computers are online.
- **Tray icon.** Closing the window leaves an icon in the tray: click it to
  open the window, right-click for the connection state, Start/Stop sharing
  and Quit (which also stops the service). `mwb-gui --background` starts with
  just the icon; the autostart entry
  `io.github.lucky_verma.MwbLinux.Tray.desktop` runs it at login. The icon is
  a StatusNotifierItem published with Gio, since GTK 4 has no tray API; with
  no tray host on the desktop, closing the window quits.

## How it treats your config

Only the settings you change are written. Everything else in the file is
kept exactly as it was: comments, other settings such as
`accel_multiplier` or `legacy_crypto`, and any tables. The file is replaced
atomically and stays readable only by you (0600), because it holds the
security key.

If the file can't be parsed safely, the window refuses to save and leaves
the file untouched.

If the service runs `mwb` with `-bidi` or `-edge` (the Debian package's unit
does), those flags override the config. The window points this out.

## Tests

```bash
make test-gui
```

These tests need no GTK. The window was also checked under Xvfb in light and
dark themes.
