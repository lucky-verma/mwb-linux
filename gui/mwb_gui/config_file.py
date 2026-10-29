"""Read and update ~/.config/mwb/config.toml without disturbing it.

The GUI manages a handful of top-level keys. Everything else in the file
(comments, ordering, keys it does not know, tables) must survive a save
byte for byte, so this module edits the file line by line instead of
re-serialising a parsed document. Only the value of a managed key is
replaced; a trailing comment on that line is kept.

No third-party TOML library is used: tomllib is read-only and only exists
on Python 3.11+, while Ubuntu 22.04 ships 3.10. When tomllib is available
it is used as a final check that the written document still parses and
that every managed value round-trips; otherwise this module's own parser
re-reads the result. A save that fails the check leaves the original file
untouched.
"""

from __future__ import annotations

import os
import re
import tempfile
from dataclasses import dataclass, field

try:  # Python 3.11+
    import tomllib  # type: ignore[import-not-found]
except ModuleNotFoundError:  # pragma: no cover - depends on interpreter
    tomllib = None

# Managed keys and their Python types. Defaults mirror internal/config/config.go
# so the form shows what the client will actually use.
MANAGED: dict[str, type] = {
    "host": str,
    "key": str,
    "name": str,
    "peer": str,
    "edge": str,
    "edge_changed": int,  # when edge was chosen, Unix seconds; mwb keeps two peers' sides mirrored
    "layout": list,  # Linux groups: machine names left to right; mwb keeps it in step across the group
    "layout_changed": int,  # when layout was chosen, Unix seconds
    "grid": list,  # 2D arrangement, each "name:col:row"; empty = the plain row in layout
    "interface": str,  # network interface to use, e.g. "wlp4s0"; "" = all
    "bidirectional": bool,
    "clipboard": bool,
    "file_transfer": bool,
    "remote_width": int,
    "remote_height": int,
}

DEFAULTS: dict[str, object] = {
    "host": "",
    "key": "",
    "name": "",
    "peer": "windows",
    "edge": "left",
    "edge_changed": 0,
    "layout": [],
    "layout_changed": 0,
    "grid": [],
    "interface": "",
    "bidirectional": False,
    "clipboard": True,
    "file_transfer": True,
    "remote_width": 1920,
    "remote_height": 1080,
}

NAME_MAX = 15  # the client truncates longer names; the wire field is 15 bytes
GROUP_MAX = 6  # machines in one group, this one included
KEY_MIN = 16   # PowerToys refuses shorter security keys

DEFAULT_PATH = os.path.join(
    os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config"),
    "mwb",
    "config.toml",
)


class ConfigError(Exception):
    """The file cannot be edited safely; nothing was written."""


_KEY_LINE = re.compile(r"^(?P<indent>[ \t]*)(?P<key>[A-Za-z0-9_-]+)(?P<eq>[ \t]*=[ \t]*)(?P<rest>.*)$")
_TABLE_LINE = re.compile(r"^[ \t]*\[")


@dataclass
class _Entry:
    line: int          # index into lines
    prefix: str        # indent + key + " = "
    suffix: str        # whitespace + comment after the value, without newline
    value: object


@dataclass
class Document:
    """A parsed config file that can be updated in place."""

    lines: list[str] = field(default_factory=list)
    entries: dict[str, _Entry] = field(default_factory=dict)
    top_level_end: int = 0  # index of the first table header, or len(lines)

    def values(self) -> dict[str, object]:
        out = dict(DEFAULTS)
        for k, e in self.entries.items():
            out[k] = e.value
        if "bidirectional" not in self.entries:
            # Mirrors config.BidirectionalEnabled: unset means two-way for a
            # Linux peer and receive-only for a PowerToys one.
            out["bidirectional"] = out["peer"] == "linux"
        return out


# --- value parsing -----------------------------------------------------------

_ESCAPES = {'"': '"', "\\": "\\", "b": "\b", "t": "\t", "n": "\n", "f": "\f", "r": "\r"}


def _parse_basic_string(s: str, i: int) -> tuple[str, int]:
    """Parse a "..." string starting at s[i] == '"'. Returns (value, end)."""
    out = []
    i += 1
    while i < len(s):
        c = s[i]
        if c == '"':
            return "".join(out), i + 1
        if c == "\\":
            if i + 1 >= len(s):
                break
            n = s[i + 1]
            if n in _ESCAPES:
                out.append(_ESCAPES[n])
                i += 2
                continue
            if n in "uU":
                width = 4 if n == "u" else 8
                hexpart = s[i + 2 : i + 2 + width]
                if len(hexpart) != width or not all(h in "0123456789abcdefABCDEF" for h in hexpart):
                    raise ConfigError("invalid unicode escape")
                out.append(chr(int(hexpart, 16)))
                i += 2 + width
                continue
            raise ConfigError(f"invalid escape \\{n}")
        out.append(c)
        i += 1
    raise ConfigError("unterminated string")


def _parse_string_array(text: str) -> tuple[list[str], int]:
    """Parse a one-line array of strings starting at text[0] == "["."""
    out: list[str] = []
    i = 1
    expect_item = True
    while i < len(text):
        c = text[i]
        if c in " \t":
            i += 1
        elif c == "]":
            return out, i + 1
        elif c == "," and not expect_item:
            expect_item = True
            i += 1
        elif expect_item and c in "\"'":
            value, end = _parse_value(text[i:])
            if not isinstance(value, str):
                raise ConfigError("only strings are supported in this list")
            out.append(value)
            i += end
            expect_item = False
        else:
            raise ConfigError("only a one-line list of strings is supported here")
    raise ConfigError("unterminated list")


def _parse_value(text: str) -> tuple[object, int]:
    """Parse a single-line TOML value. Returns (value, end index)."""
    if text.startswith("["):
        return _parse_string_array(text)
    if text.startswith('"""') or text.startswith("'''"):
        raise ConfigError("multi-line strings are not supported for this setting")
    if text.startswith('"'):
        return _parse_basic_string(text, 0)
    if text.startswith("'"):
        end = text.find("'", 1)
        if end < 0:
            raise ConfigError("unterminated string")
        return text[1:end], end + 1
    m = re.match(r"[^\s#,\]\}]+", text)
    if not m:
        raise ConfigError("missing value")
    token = m.group(0)
    if token == "true":
        return True, m.end()
    if token == "false":
        return False, m.end()
    try:
        return int(token.replace("_", ""), 0 if token[:2].lower() in ("0x", "0o", "0b") else 10), m.end()
    except ValueError:
        pass
    try:
        return float(token.replace("_", "")), m.end()
    except ValueError:
        pass
    raise ConfigError(f"unrecognised value {token!r}")


def _skip_multiline(lines: list[str], start: int, rest: str) -> int:
    """Return the index of the last line of a value that may span lines.

    Only needed so keys the GUI does not manage (arrays, multi-line strings)
    are stepped over rather than misread as new keys.
    """
    for quote in ('"""', "'''"):
        if rest.startswith(quote):
            if rest.count(quote) >= 2:
                return start
            for j in range(start + 1, len(lines)):
                if quote in lines[j]:
                    return j
            return len(lines) - 1
    if rest.startswith("[") or rest.startswith("{"):
        depth, in_str = 0, None
        j, text = start, rest
        while True:
            k = 0
            while k < len(text):
                c = text[k]
                if in_str:
                    if c == "\\" and in_str == '"':
                        k += 2
                        continue
                    if c == in_str:
                        in_str = None
                elif c in "\"'":
                    in_str = c
                elif c == "#":
                    break
                elif c in "[{":
                    depth += 1
                elif c in "]}":
                    depth -= 1
                    if depth == 0:
                        return j
                k += 1
            j += 1
            if j >= len(lines):
                return len(lines) - 1
            text = lines[j]
    return start


def parse(text: str) -> Document:
    lines = text.splitlines(keepends=True)
    doc = Document(lines=lines, top_level_end=len(lines))
    i = 0
    while i < len(lines):
        raw = lines[i].rstrip("\r\n")
        if _TABLE_LINE.match(raw):
            doc.top_level_end = i
            break
        m = _KEY_LINE.match(raw)
        if not m:
            i += 1
            continue
        key, rest = m.group("key"), m.group("rest")
        if key in MANAGED:
            if key in doc.entries:
                raise ConfigError(f"{key} is set more than once")
            try:
                value, end = _parse_value(rest)
            except ConfigError as exc:
                raise ConfigError(f"{key}: {exc}") from None
            want = MANAGED[key]
            if want is int and isinstance(value, bool) or not isinstance(value, want):
                raise ConfigError(f"{key} should be a {want.__name__}")
            doc.entries[key] = _Entry(
                line=i,
                prefix=m.group("indent") + key + m.group("eq"),
                suffix=rest[end:],
                value=value,
            )
            i += 1
            continue
        i = _skip_multiline(lines, i, rest) + 1
    return doc


# --- value formatting --------------------------------------------------------

def _format_value(value: object) -> str:
    if isinstance(value, list):
        return "[" + ", ".join(_format_value(str(v)) for v in value) + "]"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, str):
        out = ['"']
        for c in value:
            if c == '"':
                out.append('\\"')
            elif c == "\\":
                out.append("\\\\")
            elif c == "\n":
                out.append("\\n")
            elif c == "\t":
                out.append("\\t")
            elif ord(c) < 0x20 or ord(c) == 0x7F:
                out.append(f"\\u{ord(c):04X}")
            else:
                out.append(c)
        out.append('"')
        return "".join(out)
    raise TypeError(f"unsupported value {value!r}")


def render(doc: Document, updates: dict[str, object]) -> str:
    """Return the file text with updates applied. doc is not modified."""
    lines = list(doc.lines)
    appended: list[str] = []
    for key in MANAGED:  # stable order for new keys
        if key not in updates:
            continue
        value = updates[key]
        want = MANAGED[key]
        if want is int and isinstance(value, bool) or not isinstance(value, want):
            raise ConfigError(f"{key} should be a {want.__name__}")
        entry = doc.entries.get(key)
        if entry is not None:
            if entry.value == value and type(entry.value) is type(value):
                continue  # untouched keys stay byte-identical
            ending = lines[entry.line][len(lines[entry.line].rstrip("\r\n")) :] or "\n"
            lines[entry.line] = entry.prefix + _format_value(value) + entry.suffix + ending
        else:
            appended.append(f"{key} = {_format_value(value)}\n")

    if appended:
        at = doc.top_level_end
        # Keep keys above the first table and after the last top-level content.
        while at > 0 and lines[at - 1].strip() == "":
            at -= 1
        if at > 0 and not lines[at - 1].endswith("\n"):
            lines[at - 1] += "\n"
        block = appended
        # Separate from a following table, unless a blank line already does.
        if at < len(lines) and lines[at].strip() != "":
            block = appended + ["\n"]
        lines[at:at] = block
    return "".join(lines)


# --- file I/O ----------------------------------------------------------------

def load(path: str = DEFAULT_PATH) -> Document:
    """Load path, or an empty document if it does not exist yet."""
    try:
        with open(path, encoding="utf-8") as f:
            text = f.read()
    except FileNotFoundError:
        return Document()
    return parse(text)


def _verify(text: str, updates: dict[str, object]) -> None:
    if tomllib is not None:
        try:
            data = tomllib.loads(text)
        except tomllib.TOMLDecodeError as exc:
            raise ConfigError(f"the updated file would not be valid TOML ({exc})") from None
    else:
        data = {k: e.value for k, e in parse(text).entries.items()}
    for k, v in updates.items():
        if data.get(k) != v:
            raise ConfigError(f"{k} did not survive the update")


def save(updates: dict[str, object], path: str = DEFAULT_PATH) -> None:
    """Apply updates to path atomically, owner-only. Raises ConfigError."""
    real = os.path.realpath(path)  # write through a symlinked config
    doc = load(real)
    text = render(doc, updates)
    if not doc.lines:
        text = (
            "# Mouse Without Borders for Linux. Edited by mwb-gui;\n"
            "# comments and settings it does not show are kept.\n" + text
        )
    _verify(text, updates)

    directory = os.path.dirname(real)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    # The file holds the security key: create the replacement 0600 from the
    # start so it is never briefly readable by other users.
    fd, tmp = tempfile.mkstemp(prefix=".config-", suffix=".toml", dir=directory)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, real)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def members_path() -> str:
    """Where mwb keeps the addresses of the other computers it learned
    (internal/group StorePath)."""
    state = os.environ.get("XDG_STATE_HOME") or ""
    if not os.path.isabs(state):
        state = os.path.join(os.path.expanduser("~"), ".local", "state")
    return os.path.join(state, "mwb", "members.json")


def clear(path: str = DEFAULT_PATH, members: str | None = None) -> str:
    """Remove the saved settings: the config file is renamed to
    config.toml.bak (replacing an older backup), and the remembered
    computers are forgotten. Returns the backup path, or "" if there was no
    config. Raises OSError."""
    real = os.path.realpath(path)
    backup = ""
    if os.path.exists(real):
        backup = real + ".bak"
        os.replace(real, backup)
        os.chmod(backup, 0o600)  # it still holds the security key
    try:
        os.unlink(members if members is not None else members_path())
    except FileNotFoundError:
        pass
    return backup


def validate(values: dict[str, object]) -> dict[str, str]:
    """Return {key: message} for values the client would reject or misuse."""
    problems: dict[str, str] = {}
    if not str(values.get("host", "")).strip() and values.get("peer") != "linux":
        # A Linux peer may leave it empty to wait for the other computer.
        problems["host"] = "Enter the other computer's address."
    if not str(values.get("key", "")):
        problems["key"] = "Enter the security key, or generate one."
    name = str(values.get("name", ""))
    if len(name.encode("utf-8")) > NAME_MAX:
        problems["name"] = f"Use {NAME_MAX} characters or fewer."
    if values.get("peer") not in ("windows", "linux"):
        problems["peer"] = "Choose Windows or Linux."
    if values.get("edge") not in ("left", "right"):
        problems["edge"] = "Choose left or right."
    layout = values.get("layout") or []
    if not isinstance(layout, list) or len(layout) > GROUP_MAX or len(set(layout)) != len(layout) \
            or any(not isinstance(n, str) or not n or len(n.encode("utf-8")) > NAME_MAX for n in layout):
        problems["layout"] = f"The screen order needs up to {GROUP_MAX} different computer names."
    grid = values.get("grid") or []
    if not isinstance(grid, list) or len(grid) > GROUP_MAX or not _grid_ok(grid):
        problems["grid"] = f"The arrangement needs up to {GROUP_MAX} computers, each as name:col:row."
    for k in ("remote_width", "remote_height"):
        v = values.get(k)
        if not isinstance(v, int) or isinstance(v, bool) or not 200 <= v <= 16384:
            problems[k] = "Enter a screen size between 200 and 16384 pixels."
    return problems


def _grid_ok(grid: list) -> bool:
    """Whether every grid entry is a valid "name:col:row" with a real name and
    non-negative bounds, and the names are distinct."""
    names = []
    for e in grid:
        if not isinstance(e, str):
            return False
        parts = e.split(":")
        if len(parts) != 3 or not parts[0] or len(parts[0].encode("utf-8")) > NAME_MAX:
            return False
        try:
            c, r = int(parts[1]), int(parts[2])
        except ValueError:
            return False
        if c < 0 or r < 0 or c >= GROUP_MAX or r >= GROUP_MAX:
            return False
        names.append(parts[0])
    return len(set(names)) == len(names)


def warnings(values: dict[str, object]) -> dict[str, str]:
    """Return {key: message} for values that work but are likely mistakes.

    These never block saving: the client accepts them, and a stricter GUI
    would lock out an existing working setup.
    """
    out: dict[str, str] = {}
    key = str(values.get("key", ""))
    if key and len(key) < KEY_MIN:
        out["key"] = f"PowerToys needs at least {KEY_MIN} characters. A longer key is also harder to guess."
    return out
