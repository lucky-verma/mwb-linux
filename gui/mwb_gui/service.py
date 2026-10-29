"""Talk to the mwb systemd user service and read the client's status file.

Everything here shells out to `systemctl --user` / `journalctl --user` with
fixed argument lists (no shell), and only for the user's own session: the
GUI never needs or asks for root.
"""

from __future__ import annotations

import grp
import ipaddress
import json
from datetime import datetime, timezone
import os
import pwd
import shlex
import shutil
import socket
import subprocess
import time
from dataclasses import dataclass

UNIT = "mwb.service"
_TIMEOUT = 10  # seconds; systemctl answers in milliseconds when healthy


@dataclass
class UnitState:
    installed: bool = False
    active: str = "unknown"      # active, inactive, failed, activating, ...
    sub: str = ""                # running, auto-restart, dead, failed, ...
    enabled: bool = False
    exec_args: list[str] | None = None  # the ExecStart command line, if known
    error: str = ""

    @property
    def crash_looping(self) -> bool:
        """Exited with an error and waiting for systemd to start it again.

        systemd reports this as "activating"/"auto-restart", which must not
        be shown as running: the client is not up at all.
        """
        return self.sub == "auto-restart"

    @property
    def running(self) -> bool:
        if self.crash_looping:
            return False
        return self.active in ("active", "activating", "reloading")

    def overriding_flags(self) -> list[str]:
        """Flags in ExecStart that override settings the GUI edits."""
        flags = []
        for arg in self.exec_args or []:
            name = arg.split("=", 1)[0].lstrip("-")
            if name in ("bidi", "edge", "no-clipboard", "config") and arg.startswith("-"):
                flags.append(arg)
        return flags


def _systemctl(*args: str) -> subprocess.CompletedProcess:
    exe = shutil.which("systemctl")
    if exe is None:
        raise FileNotFoundError("systemctl is not available on this system")
    return subprocess.run(
        [exe, "--user", *args],
        capture_output=True,
        text=True,
        timeout=_TIMEOUT,
        check=False,
    )


def _parse_exec_start(value: str) -> list[str] | None:
    # systemd prints: { path=/usr/bin/mwb ; argv[]=/usr/bin/mwb -bidi -edge left ; ... }
    marker = "argv[]="
    start = value.find(marker)
    if start < 0:
        return None
    end = value.find(" ;", start)
    argv = value[start + len(marker) : end if end >= 0 else None]
    try:
        return shlex.split(argv)
    except ValueError:
        return argv.split()


def query() -> UnitState:
    try:
        r = _systemctl("show", UNIT, "--property=LoadState,ActiveState,SubState,UnitFileState,ExecStart")
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError) as exc:
        return UnitState(error=str(exc))
    if r.returncode != 0:
        return UnitState(error=(r.stderr or r.stdout).strip() or "systemctl failed")
    props = {}
    for line in r.stdout.splitlines():
        k, _, v = line.partition("=")
        props[k] = v
    return UnitState(
        installed=props.get("LoadState") == "loaded",
        active=props.get("ActiveState", "unknown"),
        sub=props.get("SubState", ""),
        enabled=props.get("UnitFileState") in ("enabled", "enabled-runtime"),
        exec_args=_parse_exec_start(props.get("ExecStart", "")),
    )


def control(action: str) -> str:
    """Run start/stop/restart/enable/disable. Returns "" or an error message."""
    if action not in ("start", "stop", "restart", "enable", "disable"):
        raise ValueError(action)
    try:
        r = _systemctl(action, UNIT)
    except (FileNotFoundError, subprocess.TimeoutExpired, OSError) as exc:
        return str(exc)
    if r.returncode != 0:
        return (r.stderr or r.stdout).strip() or f"systemctl {action} failed"
    return ""


def recent_log(lines: int = 15) -> str:
    exe = shutil.which("journalctl")
    if exe is None:
        return "journalctl is not available on this system."
    try:
        r = subprocess.run(
            [exe, "--user", "-u", UNIT, "-n", str(lines), "--no-pager", "-o", "cat"],
            capture_output=True,
            text=True,
            timeout=_TIMEOUT,
            check=False,
        )
    except (subprocess.TimeoutExpired, OSError) as exc:
        return str(exc)
    return (r.stdout or r.stderr).strip() or "No log entries yet."


# --- /dev/uinput access ------------------------------------------------------

UINPUT = "/dev/uinput"


def diagnose_uinput(exists: bool, accessible: bool, group_exists: bool,
                    in_group_db: bool, in_group_now: bool) -> tuple[str, str] | None:
    """Return (title, fix) if mwb would be refused /dev/uinput, else None.

    Mirrors the client's own diagnosis (cmd/mwb/uinput_help.go), so the window
    can say what to do before the user digs through a log.
    """
    if not exists:
        return ("The uinput kernel module isn't loaded",
                "Run: sudo modprobe uinput")
    if accessible:
        return None
    if not group_exists:
        return ("This system has no input group",
                "Reinstall the mwb-linux package, or run: sudo groupadd --system input")
    if in_group_db and not in_group_now:
        return ("Restart the computer to finish setup",
                "You were added to the input group after this session started. "
                "Logging out is often not enough; restart the computer.")
    if not in_group_db:
        return ("Your account needs the input group",
                "Run: sudo usermod -aG input $USER, then restart the computer.")
    return ("mwb can't use /dev/uinput",
            "Check the device's permissions with: ls -l /dev/uinput. "
            "It should show the input group, as crw-rw----.")


def uinput_problem(path: str = UINPUT) -> tuple[str, str] | None:
    """Check /dev/uinput for the current user. The GUI runs in the same login
    session as the service, so its groups are the service's groups."""
    exists = os.path.exists(path)
    accessible = exists and os.access(path, os.R_OK | os.W_OK)
    group_exists = in_db = in_now = False
    if exists and not accessible:
        try:
            g = grp.getgrnam("input")
            group_exists = True
            try:
                pw = pwd.getpwuid(os.getuid())
                in_db = pw.pw_name in g.gr_mem or pw.pw_gid == g.gr_gid
            except KeyError:
                pass
            in_now = g.gr_gid in os.getgroups() or os.getegid() == g.gr_gid
        except KeyError:
            pass
    return diagnose_uinput(exists, accessible, group_exists, in_db, in_now)


# --- this computer's address -------------------------------------------------

def local_address() -> str:
    """This computer's address on its local network, or "".

    A UDP socket "connected" to a private address sends nothing; it only makes
    the kernel pick the outgoing interface, whose address is what the other
    computer types in. Only private addresses are returned, matching what a
    waiting mwb accepts connections from.
    """
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(("10.255.255.255", 1))
        ip = s.getsockname()[0]
    except OSError:
        return ""
    finally:
        s.close()
    return ip if is_lan_address(ip) else ""


# Exactly Go's net.IP.IsPrivate for IPv4 (RFC 1918), which the client uses.
# Python's is_private is broader (it includes documentation and other
# reserved ranges), so it is not used here.
_LAN_V4 = tuple(ipaddress.ip_network(n) for n in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"))


def is_lan_address(ip: str) -> bool:
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return False
    return addr.version == 4 and any(addr in n for n in _LAN_V4)


# --- network interfaces --------------------------------------------------------

# Interfaces that only carry traffic for containers and VMs on this machine.
_HIDDEN_PREFIXES = ("docker", "br-", "veth", "virbr", "vmnet", "vboxnet", "lxc", "lxd", "podman", "cni", "flannel")
_VPN_PREFIXES = ("tun", "tap", "wg", "tailscale", "zt", "ppp", "nordlynx", "proton", "mullvad")


@dataclass
class Interface:
    name: str
    kind: str            # "Wi-Fi", "Ethernet", "VPN" or "Virtual"
    up: bool
    addresses: list[str]
    hidden: bool = False  # container/VM bridges, not offered by default

    @property
    def address(self) -> str:
        """The address another computer would use, as mwb picks it."""
        return pick_address(self.addresses)

    def label(self) -> str:
        where = self.address or "not connected"
        return f"{self.kind} · {self.name} · {where}"


def pick_address(addresses: list[str]) -> str:
    """Mirrors cmd/mwb pickInterfaceIP: private IPv4, any IPv4, global IPv6."""
    parsed = []
    for a in addresses:
        try:
            parsed.append(ipaddress.ip_address(a.split("%")[0]))
        except ValueError:
            continue
    for want in (
        lambda ip: ip.version == 4 and is_lan_address(str(ip)),
        lambda ip: ip.version == 4 and not ip.is_loopback and not ip.is_link_local,
        lambda ip: ip.version == 6 and ip.is_global,
    ):
        for ip in parsed:
            if want(ip):
                return str(ip)
    return ""


def _kind(name: str, sysfs: str) -> tuple[str, bool]:
    if name.startswith(_HIDDEN_PREFIXES):
        return "Virtual", True
    if name.startswith(_VPN_PREFIXES):
        return "VPN", False
    if os.path.isdir(os.path.join(sysfs, name, "wireless")) or os.path.isdir(os.path.join(sysfs, name, "phy80211")):
        return "Wi-Fi", False
    if os.path.exists(os.path.join(sysfs, name, "device")):
        return "Ethernet", False
    return "Virtual", False


def parse_ip_json(text: str, sysfs: str = "/sys/class/net") -> list[Interface]:
    """Interfaces from `ip -j addr`, loopback left out."""
    try:
        data = json.loads(text)
    except ValueError:
        return []
    out = []
    for entry in data if isinstance(data, list) else []:
        name = str(entry.get("ifname") or "")
        if not name or "LOOPBACK" in (entry.get("flags") or []):
            continue
        addrs = [str(a.get("local")) for a in entry.get("addr_info") or []
                 if a.get("family") in ("inet", "inet6") and a.get("local")]
        kind, hidden = _kind(name, sysfs)
        up = entry.get("operstate") in ("UP", "UNKNOWN") and bool(addrs)
        out.append(Interface(name=name, kind=kind, up=up, addresses=addrs, hidden=hidden))
    order = {"Ethernet": 0, "Wi-Fi": 1, "VPN": 2, "Virtual": 3}
    out.sort(key=lambda i: (not i.up, order.get(i.kind, 9), i.name))
    return out


def list_interfaces() -> list[Interface]:
    exe = shutil.which("ip")
    if not exe:
        return []
    try:
        r = subprocess.run([exe, "-j", "addr"], capture_output=True, text=True, timeout=_TIMEOUT)
    except (subprocess.TimeoutExpired, OSError):
        return []
    return parse_ip_json(r.stdout) if r.returncode == 0 else []


# --- status file written by the client (internal/status) ---------------------

def status_path() -> str:
    run = os.environ.get("XDG_RUNTIME_DIR", "")
    if not run or not os.path.isabs(run):
        return ""
    return os.path.join(run, "mwb", "status.json")


# --- forgetting a machine (internal/group store) -----------------------------

# Longest machine name the wire carries (internal/group maxNameLen). A request
# for anything longer could never match a real member, so it is refused.
_MAX_NAME_BYTES = 15


def state_dir() -> str:
    """Where the client keeps learned members and tombstones, mirroring Go's
    group.StorePath: $XDG_STATE_HOME/mwb or ~/.local/state/mwb."""
    base = os.environ.get("XDG_STATE_HOME", "")
    if not base or not os.path.isabs(base):
        base = os.path.join(os.path.expanduser("~"), ".local", "state")
    return os.path.join(base, "mwb")


_client_version: str | None = None


def client_version() -> str:
    """The installed client's version, e.g. "0.6.4-linuxpeer.24", from
    `mwb -version`. Cached: the binary does not change while the GUI runs. The
    status file's own version field is preferred when the client is running, so
    this is the fallback for when it is stopped. "" if it cannot be read."""
    global _client_version
    if _client_version is None:
        _client_version = ""
        exe = shutil.which("mwb") or "/usr/local/bin/mwb"
        try:
            r = subprocess.run([exe, "-version"], capture_output=True, text=True,
                               timeout=_TIMEOUT, check=False)
            out = (r.stdout or "").strip()
            if out:
                _client_version = out.split()[-1]  # "mwb 0.6.4-..." -> "0.6.4-..."
        except (FileNotFoundError, subprocess.TimeoutExpired, OSError):
            pass
    return _client_version


_forgotten_cache: tuple = (0.0, frozenset())


def forgotten_names(directory: str | None = None) -> set:
    """Machines the client has been told to forget: the tombstones it has
    recorded (forgotten.json) plus any request not yet consumed
    (forget-request.json). The front end hides these from the arrangement and
    never saves them back, so a forgotten machine cannot reappear from a stale
    config or a save. Missing or corrupt files mean an empty set. The default
    (state-dir) read is cached briefly, since several helpers call it per poll."""
    global _forgotten_cache
    use_cache = directory is None
    if use_cache:
        ts, cached = _forgotten_cache
        if time.monotonic() - ts < 0.5:
            return set(cached)
    directory = state_dir() if directory is None else directory
    names: set = set()
    try:
        with open(os.path.join(directory, "forgotten.json"), encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, dict):
            names |= {str(n) for n in data}
    except (OSError, ValueError):
        pass
    try:
        with open(os.path.join(directory, "forget-request.json"), encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, list):
            names |= {str(n) for n in data if isinstance(n, str)}
    except (OSError, ValueError):
        pass
    if use_cache:
        _forgotten_cache = (time.monotonic(), frozenset(names))
    return names


def grid_extent(cells: dict) -> tuple:
    """(columns, rows) spanning a position map {name: (col, row)}; (0, 0) when
    empty. Used to lay out the two-dimensional arrangement."""
    if not cells:
        return (0, 0)
    return (max(c for c, _ in cells.values()) + 1, max(r for _, r in cells.values()) + 1)


def grid_rects(cells: dict, aspects: dict, width: float, height: float,
               pad: float = 10.0, label_h: float = 40.0) -> dict:
    """Pixel rect (x, y, w, h) for each screen placed in its grid cell, sized to
    its aspect ratio and fit inside the cell with room for a label below.
    width/height is the drawing area. Pure, so it can be tested without GTK."""
    cols, rows = grid_extent(cells)
    if cols == 0 or rows == 0:
        return {}
    cell_w, cell_h = width / cols, height / rows
    out = {}
    for name, (c, r) in cells.items():
        avail_w = max(1.0, cell_w - 2 * pad)
        avail_h = max(1.0, cell_h - 2 * pad - label_h)
        asp = aspects.get(name, 16 / 9)
        w = avail_w
        h = w / asp
        if h > avail_h:
            h, w = avail_h, avail_h * asp
        cx = c * cell_w + cell_w / 2
        top = r * cell_h + (cell_h - label_h - h) / 2
        out[name] = (cx - w / 2, top, w, h)
    return out


def renormalize(cells: dict) -> dict:
    """Snap positions back to consecutive integer columns and rows, removing any
    gaps. Fractional values sort into place first, so a half-step insert becomes
    a clean new column or row."""
    if not cells:
        return {}
    cols = sorted({c for c, _ in cells.values()})
    rows = sorted({r for _, r in cells.values()})
    cmap = {c: i for i, c in enumerate(cols)}
    rmap = {r: i for i, r in enumerate(rows)}
    return {n: (cmap[c], rmap[r]) for n, (c, r) in cells.items()}


def place_adjacent(cells: dict, name: str, target: str, side: str) -> dict:
    """Move name next to target: a new column or row on the given side
    ("left"/"right"/"up"/"down"), or swap the two when side is "center".
    Returns a fresh {name: (col, row)}. A no-op drop returns the cells
    unchanged."""
    if name not in cells or target not in cells or (name == target and side != "center"):
        return {n: (int(c), int(r)) for n, (c, r) in cells.items()}
    out = {n: (float(c), float(r)) for n, (c, r) in cells.items()}
    tc, tr = out[target]
    if side == "center":
        out[name], out[target] = out[target], out[name]
    elif side == "left":
        out[name] = (tc - 0.5, tr)
    elif side == "right":
        out[name] = (tc + 0.5, tr)
    elif side == "up":
        out[name] = (tc, tr - 0.5)
    elif side == "down":
        out[name] = (tc, tr + 0.5)
    else:
        return {n: (int(c), int(r)) for n, (c, r) in cells.items()}
    return renormalize(out)


def move_in_grid(cells: dict, name: str, side: str) -> dict:
    """Move name one cell in a direction ("left"/"right"/"up"/"down"), swapping
    with whatever is there, or opening a new column/row at the edge. Returns a
    fresh {name: (col, row)}. For arrow-key arranging."""
    out = {n: (int(c), int(r)) for n, (c, r) in cells.items()}
    if name not in out:
        return out
    delta = {"left": (-1, 0), "right": (1, 0), "up": (0, -1), "down": (0, 1)}.get(side)
    if delta is None:
        return out
    c, r = out[name]
    tc, tr = c + delta[0], r + delta[1]
    occupant = next((n for n, (cc, rr) in out.items() if (cc, rr) == (tc, tr) and n != name), None)
    out[name] = (tc, tr)
    if occupant is not None:
        out[occupant] = (c, r)  # swap
    return renormalize(out)


def drop_target(rects: dict, x: float, y: float) -> tuple:
    """Which screen a drop at (x, y) lands on and which side of it, as
    (name, side): "center" to swap, or "left"/"right"/"up"/"down" to place
    beside it. rects is {name: (x, y, w, h)}. (None, "") when there is nothing."""
    if not rects:
        return (None, "")

    def center(r):
        return (r[0] + r[2] / 2, r[1] + r[3] / 2)

    name = min(rects, key=lambda n: (center(rects[n])[0] - x) ** 2 + (center(rects[n])[1] - y) ** 2)
    rx, ry, rw, rh = rects[name]
    dx, dy = x - (rx + rw / 2), y - (ry + rh / 2)
    inside = rx <= x <= rx + rw and ry <= y <= ry + rh
    if inside and abs(dx) <= rw * 0.3 and abs(dy) <= rh * 0.3:
        return (name, "center")
    if abs(dx) / max(1, rw) >= abs(dy) / max(1, rh):
        return (name, "right" if dx > 0 else "left")
    return (name, "down" if dy > 0 else "up")


def forgettable(name: str, me: str, offline: set, running: bool, linux_group: bool) -> bool:
    """Whether a selected screen in the arrangement can be removed from the
    group. Only a real, named Linux computer other than this one; and while the
    service runs (so online state is known) only one that is offline, since an
    online computer would simply rejoin at once. With the service stopped the
    state is unknown, so any other named machine may be forgotten."""
    if not name or not linux_group or name == me:
        return False
    if running:
        return name in offline
    return True


def request_forget(name: str, directory: str | None = None) -> str:
    """Ask the client to forget a machine on its next start.

    The GUI has no live channel to the running client, so this leaves a request
    file the client consumes when it restarts, the same way a saved layout
    takes effect on restart. Returns "" on success or an error message; raises
    ValueError for a name that could never be a real machine.
    """
    name = (name or "").strip()
    if not name or len(name.encode("utf-8")) > _MAX_NAME_BYTES:
        raise ValueError(name)
    directory = state_dir() if directory is None else directory
    path = os.path.join(directory, "forget-request.json")
    names: list[str] = []
    try:
        with open(path, encoding="utf-8") as f:
            existing = json.load(f)
        if isinstance(existing, list):
            names = [str(n) for n in existing if isinstance(n, str)]
    except (OSError, ValueError):
        names = []
    if name not in names:
        names.append(name)
    try:
        os.makedirs(directory, mode=0o700, exist_ok=True)
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(names, f)
        os.replace(tmp, path)
    except OSError as exc:
        return str(exc)
    return ""


def _pid_alive(pid: object) -> bool:
    if not isinstance(pid, int) or pid <= 0:
        return False
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True  # exists, owned by someone else
    return True


def read_status(path: str | None = None) -> dict | None:
    """Return the client's status, or None if it is not running or unknown.

    A status file left behind by a crashed client is ignored, by checking
    that its recorded PID still exists.
    """
    path = status_path() if path is None else path
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f)
    except (OSError, ValueError):
        return None
    if not isinstance(data, dict) or not _pid_alive(data.get("pid")):
        return None
    return data


# how long a recorded key mismatch is treated as current. The dialing peer
# retries every few seconds, so a real mismatch keeps being re-recorded; once
# the other machine stops trying, the warning fades rather than lingering.
_KEY_MISMATCH_FRESH = 30.0


def recent_key_mismatch(st: dict | None) -> str | None:
    """The address of a machine that just tried a different security key, or
    None. mwb records the time in UTC (RFC 3339); a stamp older than
    _KEY_MISMATCH_FRESH seconds, or unparseable, counts as not current."""
    if not st:
        return None
    who = st.get("key_mismatch_from")
    when = st.get("key_mismatch_at")
    if not who or not isinstance(when, str):
        return None
    try:
        ts = datetime.fromisoformat(when.replace("Z", "+00:00"))
    except ValueError:
        return None
    age = (datetime.now(timezone.utc) - ts).total_seconds()
    if 0 <= age <= _KEY_MISMATCH_FRESH:
        return str(who)
    return None


# --- what a connected status means for the user ---------------------------------

CONTROL_SWITCH = "Control the other computer from here"


def describe_connection(st: dict) -> tuple[str, str, str]:
    """(kind, title, detail) for a connected status.

    kind is "ok", "wait" or "error". With a Linux peer that reported its
    settings, this states what actually works in each direction; otherwise it
    falls back to this machine's own setting, as before.
    """
    remote = st.get("remote") or st.get("host") or "the other computer"
    title = f"Connected to {remote}"
    peer_sharing = st.get("peer_sharing")
    if peer_sharing is None:
        # A PowerToys peer, or a Linux peer that has not reported yet.
        if st.get("bidirectional"):
            return "ok", title, "Move the pointer across the highlighted edge to switch computers."
        return "ok", title, "The other computer can control this one."

    edge = st.get("edge")
    if st.get("peer_edge") == edge:
        other_side = "right" if edge == "left" else "left"
        return ("error", f"{title}, but both put the other on the {edge}",
                f"This computer has {remote} on its {edge}, and {remote} has this one on its {edge} too. "
                f"If {remote} actually sits to the {other_side} of this computer, swap the screens here and save; "
                f"otherwise swap them on {remote}. With the same mwb version on both, the side "
                f"chosen most recently is copied to the other computer automatically.")
    here = st.get("sharing")
    if here is None:
        here = bool(st.get("bidirectional"))
    wanted_here = bool(st.get("bidirectional"))
    if here and peer_sharing:
        return "ok", title, "Move the pointer across the highlighted edge on either computer."
    if here:
        return ("ok", title,
                f"This computer controls {remote}. To control this one from there, "
                f"turn on “{CONTROL_SWITCH}” on {remote}.")
    hint = ("Sharing from this computer could not start; it needs an Xorg session."
            if wanted_here else f"To control {remote} from here, turn on “{CONTROL_SWITCH}” below.")
    if peer_sharing:
        return "ok", title, f"{remote} can control this computer. {hint}"
    return ("wait", f"{title}, but neither computer shares its mouse",
            f"Turn on “{CONTROL_SWITCH}” on at least one of them. {hint if wanted_here else ''}".strip())


def join_names(names: list[str]) -> str:
    """'a', 'a and b', 'a, b and c'."""
    names = [str(n) for n in names]
    if len(names) <= 1:
        return "".join(names)
    return ", ".join(names[:-1]) + " and " + names[-1]


def describe_group(st: dict) -> tuple[str, str, str]:
    """(kind, title, detail) for a connected group of Linux computers."""
    members = [m for m in (st.get("members") or []) if isinstance(m, dict) and m.get("name")]
    online = [str(m["name"]) for m in members if m.get("online")]
    offline = [str(m["name"]) for m in members if not m.get("online")]
    older = [str(m["name"]) for m in members if m.get("online") and not m.get("current")]
    title = f"Connected to {join_names(online)}" if online else "Looking for the other computers…"
    kind = "ok" if online else "wait"
    parts = []
    if st.get("sharing"):
        parts.append("Move the pointer past a highlighted edge to use another computer.")
    else:
        parts.append(f"This keyboard and mouse stay here; the others can still control this computer. "
                     f"Turn on “{CONTROL_SWITCH}” to use them on the others.")
    if offline:
        verb = "is" if len(offline) == 1 else "are"
        parts.append(f"{join_names(offline)} {verb} offline; the pointer skips "
                     f"{'its screen' if len(offline) == 1 else 'their screens'}.")
    # The screen order can be set on any machine and the most recent choice
    # wins everywhere. When it was set on another machine, a shared edge may
    # sit on the opposite side from what this machine expects, so say where it
    # came from and how to change it, rather than letting the crossing look
    # broken. (A machine's own name is never in members, so a layout_by found
    # among them was a peer's choice.)
    by = str(st.get("layout_by") or "")
    if by and by in {str(m["name"]) for m in members}:
        parts.append(f"The screen order was set on {by}. If a computer is on the wrong side, "
                     "drag the screens below into order and Save.")
    if older:
        kind = "error"
        verb = "runs" if len(older) == 1 else "run"
        parts.append(f"{join_names(older)} {verb} an older mwb that can’t join a group. Install this version there too.")
    return kind, title, " ".join(parts)
