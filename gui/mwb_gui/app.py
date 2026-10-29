"""GTK 4 settings and status window for mwb-linux.

Works with GTK 4.6 (Ubuntu 22.04) and newer, using only GTK itself so the
window takes the desktop's own theme, including dark mode.
"""

from __future__ import annotations

import math
import os
import secrets
import socket
import sys
import threading
import time

import gi

gi.require_version("Gtk", "4.0")
gi.require_version("Gdk", "4.0")
gi.require_version("PangoCairo", "1.0")
from gi.repository import Gdk, Gio, GLib, Gtk, Pango, PangoCairo  # noqa: E402

from . import __version__  # noqa: E402
from . import config_file as cf  # noqa: E402
from . import service  # noqa: E402
from . import tray  # noqa: E402

APP_ID = "io.github.lucky_verma.MwbLinux"

# Matches internal/config/keygen.go so keys look the same from either tool.
KEY_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
KEY_LENGTH = 24

PEERS = [
    ("windows", "Windows with PowerToys"),
    ("linux", "Linux with mwb-linux"),
]
PEER_HELP = {
    "windows": "Get the security key from PowerToys → Mouse Without Borders on the Windows PC, "
    "and turn off “Move mouse relatively” there.",
    "linux": "Set the other computer to Linux too, with the same security key "
    "and a different computer name.",
}

STATUS_POLL_SECONDS = 2

# Arrangement keys for screens that have no machine name yet.
ME = "\x00me"
OTHER = "\x00other"


def generate_key() -> str:
    return "".join(secrets.choice(KEY_ALPHABET) for _ in range(KEY_LENGTH))


def _theme_colors(widget: Gtk.Widget) -> tuple[Gdk.RGBA, Gdk.RGBA]:
    """Foreground and accent colours from the current theme."""
    if hasattr(widget, "get_color"):  # GTK 4.10+
        fg = widget.get_color()
    else:
        fg = widget.get_style_context().get_color()
    ctx = widget.get_style_context()
    for name in ("accent_bg_color", "accent_color", "theme_selected_bg_color"):
        try:
            found, rgba = ctx.lookup_color(name)
        except (AttributeError, TypeError):
            found = False
        if found:
            return fg, rgba
    accent = Gdk.RGBA()
    accent.parse("#3584e4")
    return fg, accent


def _rounded_rect(cr, x, y, w, h, r):
    r = min(r, w / 2, h / 2)
    cr.new_sub_path()
    cr.arc(x + w - r, y + r, r, -math.pi / 2, 0)
    cr.arc(x + w - r, y + h - r, r, 0, math.pi / 2)
    cr.arc(x + r, y + h - r, r, math.pi / 2, math.pi)
    cr.arc(x + r, y + r, r, math.pi, 3 * math.pi / 2)
    cr.close_path()


class ArrangementView(Gtk.DrawingArea):
    """The screens in a row, left to right, as they stand on the desk.

    With two screens a click swaps them. With more, drag a screen to its
    place, or select one and move it with the Left and Right keys. Screens
    are drawn in their real proportions and touch where the pointer crosses.
    """

    DRAG_THRESHOLD = 6
    ROW_HEIGHT = 150  # pixels per grid row when showing a 2D arrangement

    def __init__(self, on_changed, on_select=None, on_grid_changed=None):
        super().__init__()
        self.order: list[str] = [ME, OTHER]
        self.cells: dict[str, tuple[int, int]] = {}  # name -> (col,row); empty = plain row
        self._on_grid_changed = on_grid_changed
        self.me = ME
        self.titles: dict[str, str] = {}
        self.kinds: dict[str, str] = {}
        self.aspects: dict[str, float] = {}
        self.offline: set[str] = set()
        self.location = ""
        self.connected = False
        self._on_changed = on_changed
        self._on_select = on_select
        self._selected = ME
        self._drag = None  # (index, start_x, current_dx) while dragging
        self._rects: list[tuple[str, float, float]] = []  # (name, x, w) as last drawn
        self._grid_rects: dict[str, tuple[float, float, float, float]] = {}  # name -> (x,y,w,h), 2D draw

        self.set_content_height(176)
        self.set_hexpand(True)
        self.set_focusable(True)
        self.set_draw_func(self._draw)

        drag = Gtk.GestureDrag()
        drag.connect("drag-begin", self._drag_begin)
        drag.connect("drag-update", self._drag_update)
        drag.connect("drag-end", self._drag_end)
        self.add_controller(drag)
        keys = Gtk.EventControllerKey()
        keys.connect("key-pressed", self._key)
        self.add_controller(keys)
        focus = Gtk.EventControllerFocus()
        focus.connect("enter", lambda *_: self.queue_draw())
        focus.connect("leave", lambda *_: self.queue_draw())
        self.add_controller(focus)
        self._update_accessible()

    # --- state -------------------------------------------------------------

    def set_order(self, order: list[str], me: str):
        order = list(order) or [me]
        if me not in order:
            order.append(me)
        if order == self.order and me == self.me and not self.cells:
            return
        self.order, self.me, self.cells = order, me, {}
        if self._selected not in order:
            self._selected = me
        self.set_content_height(176)  # back to the compact single-row height
        self.refresh()

    def set_grid(self, cells: dict, me: str):
        """Show a two-dimensional arrangement: {name: (col, row)}. order is kept
        as the row projection (top-to-bottom, left-to-right) for the parts that
        still think in a row."""
        cells = {str(n): (int(c), int(r)) for n, (c, r) in cells.items()}
        if me not in cells:
            cells[me] = (max((c for c, _ in cells.values()), default=-1) + 1, 0)
        order = sorted(cells, key=lambda n: (cells[n][1], cells[n][0]))
        if cells == self.cells and me == self.me:
            return
        self.cells, self.order, self.me = cells, order, me
        if self._selected not in cells:
            self._selected = me
        # Grow with the number of rows so each screen gets room (a single row
        # keeps the compact height).
        rows = max((r for _, r in cells.values()), default=0) + 1
        self.set_content_height(176 if rows <= 1 else rows * self.ROW_HEIGHT)
        self.refresh()

    @property
    def selected(self) -> str:
        return self._selected

    def _select(self, name: str):
        self._selected = name
        self.queue_draw()
        if self._on_select is not None:
            self._on_select(name)

    def remove_screen(self, name: str):
        """Drop a screen from the row at once, so a forgotten machine leaves the
        picture without waiting for the service to restart and report back."""
        if name == self.me or (name not in self.order and name not in self.cells):
            return
        self.order = [n for n in self.order if n != name]
        if name in self.cells:
            del self.cells[name]
        self.offline.discard(name)
        if self._selected == name:
            self._selected = self.me
        self.refresh()

    def title(self, name: str) -> str:
        if self.titles.get(name):
            return self.titles[name]
        if name in (ME, OTHER):
            return "This computer" if name == self.me else "Other computer"
        return name

    def refresh(self):
        self._update_accessible()
        self.queue_draw()

    def _update_accessible(self):
        names = [self.title(n) + (" (this computer)" if n == self.me else "") for n in self.order]
        text = "Screens from left to right: " + ", ".join(names) + "."
        if len(self.order) == 2:
            self.set_tooltip_text("Click to swap the two screens")
            text += " Press Left or Right to move this computer."
        else:
            self.set_tooltip_text("Drag a screen to move it")
            text += " Press Left or Right to move the selected screen."
        try:
            self.update_property([Gtk.AccessibleProperty.LABEL], [text])
        except (AttributeError, TypeError):
            pass

    def _move(self, name: str, to: int):
        i = self.order.index(name)
        to = max(0, min(len(self.order) - 1, to))
        if to == i:
            return
        order = list(self.order)
        order.insert(to, order.pop(i))
        self.order = order
        self.refresh()
        self._on_changed(order)

    # --- input -------------------------------------------------------------

    def _is_grid(self) -> bool:
        # Any time the view holds cells (a Linux group) it uses the 2D grid, so a
        # screen can be dragged above or below another even from a single row.
        return bool(self.cells)

    def _cell_at(self, x: float, y: float) -> str:
        for name, (rx, ry, rw, rh) in self._grid_rects.items():
            if rx <= x < rx + rw and ry <= y < ry + rh:
                return name
        return ""

    def _index_at(self, x: float) -> int:
        for i, (_n, sx, w) in enumerate(self._rects):
            if sx <= x < sx + w:
                return i
        return -1

    def _drag_begin(self, _g, x, y):
        self.grab_focus()
        if self._is_grid():
            # ("grid", dragged-name, start_x, start_y, dx, dy)
            self._drag = ("grid", self._cell_at(x, y), x, y, 0.0, 0.0)
            return
        i = self._index_at(x)
        self._drag = (i, x, 0.0) if i >= 0 else None

    def _drag_update(self, _g, dx, dy):
        if self._drag is None:
            return
        if self._drag[0] == "grid":
            _, name, x0, y0, _, _ = self._drag
            self._drag = ("grid", name, x0, y0, dx, dy)
            if name:
                self.queue_draw()
            return
        if len(self.order) > 2:
            i, x0, _ = self._drag
            self._drag = (i, x0, dx)
            self.queue_draw()

    def _drag_end(self, _g, dx, dy):
        drag, self._drag = self._drag, None
        if drag is None:
            self.queue_draw()
            return
        if drag[0] == "grid":
            _, name, x0, y0, _, _ = drag
            if abs(dx) < self.DRAG_THRESHOLD and abs(dy) < self.DRAG_THRESHOLD:
                if name:
                    self._select(name)
                self.queue_draw()
                return
            if name:
                target, side = service.drop_target(self._grid_rects, x0 + dx, y0 + dy)
                if target and (target != name or side == "center"):
                    cells = service.place_adjacent(self.cells, name, target, side)
                    self.set_grid(cells, self.me)
                    if self._on_grid_changed is not None:
                        self._on_grid_changed(dict(self.cells))
            self.queue_draw()
            return
        i, x0, _ = drag
        name = self.order[i]
        if abs(dx) < self.DRAG_THRESHOLD:
            # A click selects the screen (so it can be acted on, e.g. Forget).
            # With two screens a click also swaps their order, as before.
            if len(self.order) == 2:
                self._move(name, 1 - i)
            self._select(name)
            return
        if len(self.order) > 2:
            self._move(name, self._slot_for(x0 + dx))

    def _slot_for(self, x: float) -> int:
        """The position a screen dropped at x takes."""
        for j, (_n, sx, w) in enumerate(self._rects):
            if x < sx + w / 2:
                return j
        return len(self._rects) - 1

    def _key(self, _ctl, keyval, _code, _state):
        if self._is_grid():
            name = self._selected
            if name not in self.cells:
                return False
            side = {Gdk.KEY_Left: "left", Gdk.KEY_KP_Left: "left",
                    Gdk.KEY_Right: "right", Gdk.KEY_KP_Right: "right",
                    Gdk.KEY_Up: "up", Gdk.KEY_KP_Up: "up",
                    Gdk.KEY_Down: "down", Gdk.KEY_KP_Down: "down"}.get(keyval)
            if side is None:
                return False
            cells = service.move_in_grid(self.cells, name, side)
            if cells != self.cells:
                self.set_grid(cells, self.me)
                if self._on_grid_changed is not None:
                    self._on_grid_changed(dict(self.cells))
            return True
        name = self.me if len(self.order) == 2 else self._selected
        if keyval in (Gdk.KEY_Left, Gdk.KEY_KP_Left):
            self._move(name, self.order.index(name) - 1)
            return True
        if keyval in (Gdk.KEY_Right, Gdk.KEY_KP_Right):
            self._move(name, self.order.index(name) + 1)
            return True
        return False

    # --- drawing -----------------------------------------------------------

    def _text(self, cr, text, x, y, fg, alpha, bold=False, align_center=True, max_w=None):
        layout = PangoCairo.create_layout(cr)
        desc = self.get_pango_context().get_font_description().copy()
        if bold:
            desc.set_weight(Pango.Weight.BOLD)
        layout.set_font_description(desc)
        layout.set_text(text, -1)
        if max_w:
            layout.set_width(int(max(1, max_w) * Pango.SCALE))
            layout.set_ellipsize(Pango.EllipsizeMode.END)
            layout.set_alignment(Pango.Alignment.CENTER)
        w, h = layout.get_pixel_size()
        if max_w:
            x -= max_w / 2
        elif align_center:
            x -= w / 2
        cr.set_source_rgba(fg.red, fg.green, fg.blue, alpha)
        cr.move_to(x, y)
        PangoCairo.show_layout(cr, layout)
        return h

    def _draw(self, _area, cr, width, height):
        if self._is_grid():
            self._draw_grid(cr, width, height)
            return
        self._grid_rects = {}
        fg, accent = _theme_colors(self)
        label_h = 44
        max_h = height - label_h - 16
        widths = [max_h * self.aspects.get(n, 16 / 9) for n in self.order]
        scale = min(1.0, (width - 32) / max(1, sum(widths)))
        hgt = max_h * scale
        widths = [w * scale for w in widths]
        x0 = (width - sum(widths)) / 2
        top = 8
        base = top + hgt  # bottom-aligned, like monitors standing on a desk

        self._rects = []
        x = x0
        for n, w in zip(self.order, widths):
            self._rects.append((n, x, w))
            x += w

        # While dragging, draw the others where they would end up.
        drawn = list(self._rects)
        dragging = None
        if self._drag is not None and len(self.order) > 2:
            i, x0d, dx = self._drag
            name = self.order[i]
            slot = self._slot_for(x0d + dx)
            preview = [n for n in self.order if n != name]
            preview.insert(slot, name)
            pw = {n: w for n, _x, w in self._rects}
            drawn, x = [], x0
            for n in preview:
                drawn.append((n, x, pw[n]))
                x += pw[n]
            # Follow the pointer, but stay inside the picture.
            dragging = (name, max(0.0, min(width - pw[name], self._rects[i][1] + dx)), pw[name])

        def screen(name, sx, w, lifted=False):
            local = name == self.me
            away = name in self.offline
            _rounded_rect(cr, sx + 1, base - hgt, w - 2, hgt, 8)
            fill, stroke = (accent, accent) if local else (fg, fg)
            cr.set_source_rgba(fill.red, fill.green, fill.blue, 0.16 if local else (0.02 if away else 0.05))
            cr.fill_preserve()
            cr.set_source_rgba(stroke.red, stroke.green, stroke.blue,
                               0.9 if local or lifted else (0.2 if away else 0.35))
            cr.set_line_width(2.5 if name == self._selected and len(self.order) > 2 and self.has_focus() else 1.5)
            if away:
                cr.set_dash([5, 4])
            cr.stroke()
            cr.set_dash([])
            ty = base + 6
            label_w = max(8, w - 8)
            self._text(cr, self.title(name), sx + w / 2, ty, fg, 0.95 if not away else 0.6,
                       bold=True, max_w=label_w)
            sub = "This computer" if local else ("Offline" if away else self.kinds.get(name, "Linux computer"))
            if name == self.location and self.connected:
                sub = "Pointer is here" if not local else "This computer · pointer here"
            self._text(cr, sub, sx + w / 2, ty + 20, fg, 0.6, max_w=label_w)

        for name, sx, w in drawn:
            if dragging is None or name != dragging[0]:
                screen(name, sx, w)

        # The seams: where the pointer crosses between neighbours.
        for (a, ax, aw), (b, _bx, _bw) in zip(drawn, drawn[1:]):
            seam_x = ax + aw
            touches_me = self.me in (a, b)
            live = a not in self.offline and b not in self.offline
            cr.set_source_rgba(accent.red, accent.green, accent.blue, 1.0 if touches_me else 0.45)
            cr.set_line_width(4 if touches_me else 3)
            cr.set_line_cap(1)  # round
            cr.move_to(seam_x, base - hgt + 10)
            cr.line_to(seam_x, base - 10)
            cr.stroke()
            if self.connected and live:
                # A small filled marker on the seam shows the link is live.
                cr.arc(seam_x, base - hgt / 2, 6 if touches_me else 4, 0, 2 * math.pi)
                cr.fill()

        if dragging is not None:
            name, sx, w = dragging
            screen(name, sx, w, lifted=True)

        if self.has_focus():
            _rounded_rect(cr, 2, 2, width - 4, height - 4, 10)
            cr.set_source_rgba(accent.red, accent.green, accent.blue, 0.7)
            cr.set_line_width(2)
            cr.stroke()

    def _grid_drop_hint(self, cr, rect, side, accent):
        x, y, w, h = rect
        if side == "center":
            _rounded_rect(cr, x, y, w, h, 8)
            cr.set_source_rgba(accent.red, accent.green, accent.blue, 0.20)
            cr.fill()
            return
        cr.set_source_rgba(accent.red, accent.green, accent.blue, 0.9)
        cr.set_line_width(4)
        cr.set_line_cap(1)
        if side == "left":
            cr.move_to(x - 5, y); cr.line_to(x - 5, y + h)
        elif side == "right":
            cr.move_to(x + w + 5, y); cr.line_to(x + w + 5, y + h)
        elif side == "up":
            cr.move_to(x, y - 5); cr.line_to(x + w, y - 5)
        elif side == "down":
            cr.move_to(x, y + h + 5); cr.line_to(x + w, y + h + 5)
        cr.stroke()

    def _draw_grid(self, cr, width, height):
        """Draw the screens in two dimensions, each in its grid cell, with
        connectors where neighbouring screens touch."""
        fg, accent = _theme_colors(self)
        rects = service.grid_rects(self.cells, self.aspects, width, height, pad=12, label_h=40)
        self._grid_rects = rects

        # Connectors between cells that are side by side or stacked.
        at = {cell: name for name, cell in self.cells.items()}
        for name, (c, r) in self.cells.items():
            if name not in rects:
                continue
            x, y, w, h = rects[name]
            for dc, dr in ((1, 0), (0, 1)):  # right and down neighbours only (each pair once)
                nb = at.get((c + dc, r + dr))
                if nb is None or nb not in rects:
                    continue
                nx, ny, nw, nh = rects[nb]
                touches_me = self.me in (name, nb)
                live = name not in self.offline and nb not in self.offline
                cr.set_source_rgba(accent.red, accent.green, accent.blue, 1.0 if touches_me else 0.45)
                cr.set_line_width(4 if touches_me else 3)
                cr.set_line_cap(1)
                if dc == 1:  # horizontal neighbour: vertical seam between them
                    sx = (x + w + nx) / 2
                    y0, y1 = max(y, ny), min(y + h, ny + nh)
                    cr.move_to(sx, y0 + 6)
                    cr.line_to(sx, y1 - 6)
                else:  # vertical neighbour: horizontal seam between them
                    sy = (y + h + ny) / 2
                    x0, x1 = max(x, nx), min(x + w, nx + nw)
                    cr.move_to(x0 + 6, sy)
                    cr.line_to(x1 - 6, sy)
                cr.stroke()

        # A screen being dragged follows the pointer; highlight where it will land.
        drag_name, ddx, ddy = "", 0.0, 0.0
        if (self._drag is not None and self._drag[0] == "grid" and self._drag[1]
                and (abs(self._drag[4]) >= self.DRAG_THRESHOLD or abs(self._drag[5]) >= self.DRAG_THRESHOLD)):
            drag_name, ddx, ddy = self._drag[1], self._drag[4], self._drag[5]
            target, side = service.drop_target(rects, self._drag[2] + ddx, self._drag[3] + ddy)
            if target and target in rects:
                self._grid_drop_hint(cr, rects[target], side, accent)

        def draw_screen(name, x, y, w, h, lifted=False):
            local = name == self.me
            away = name in self.offline
            _rounded_rect(cr, x, y, w, h, 8)
            fill, stroke = (accent, accent) if local else (fg, fg)
            cr.set_source_rgba(fill.red, fill.green, fill.blue, 0.16 if local else (0.02 if away else 0.05))
            cr.fill_preserve()
            cr.set_source_rgba(stroke.red, stroke.green, stroke.blue,
                               0.9 if local or lifted else (0.2 if away else 0.35))
            cr.set_line_width(2.5 if (lifted or (name == self._selected and self.has_focus())) else 1.5)
            if away:
                cr.set_dash([5, 4])
            cr.stroke()
            cr.set_dash([])
            label_w = max(8, w - 8)
            self._text(cr, self.title(name), x + w / 2, y + h + 4, fg, 0.95 if not away else 0.6,
                       bold=True, max_w=label_w)
            sub = "This computer" if local else ("Offline" if away else self.kinds.get(name, "Linux computer"))
            if name == self.location and self.connected:
                sub = "Pointer is here" if not local else "This computer · pointer here"
            self._text(cr, sub, x + w / 2, y + h + 22, fg, 0.6, max_w=label_w)

        for name, (x, y, w, h) in rects.items():
            if name == drag_name:
                continue  # drawn last, lifted, following the pointer
            draw_screen(name, x, y, w, h)
        if drag_name and drag_name in rects:
            x, y, w, h = rects[drag_name]
            draw_screen(drag_name, x + ddx, y + ddy, w, h, lifted=True)

        if self.has_focus():
            _rounded_rect(cr, 2, 2, width - 4, height - 4, 10)
            cr.set_source_rgba(accent.red, accent.green, accent.blue, 0.7)
            cr.set_line_width(2)
            cr.stroke()


class StatusDot(Gtk.DrawingArea):
    COLORS = {
        "ok": "#2ec27e",
        "wait": "#e5a50a",
        "off": None,  # theme foreground, dimmed
        "error": "#e01b24",
    }

    def __init__(self):
        super().__init__()
        self.kind = "off"
        self.set_content_width(12)
        self.set_content_height(12)
        self.set_valign(Gtk.Align.CENTER)
        self.set_draw_func(self._draw)

    def set_kind(self, kind):
        if kind != self.kind:
            self.kind = kind
            self.queue_draw()

    def _draw(self, _a, cr, w, h):
        hexcolor = self.COLORS[self.kind]
        if hexcolor is None:
            fg, _ = _theme_colors(self)
            cr.set_source_rgba(fg.red, fg.green, fg.blue, 0.35)
        else:
            c = Gdk.RGBA()
            c.parse(hexcolor)
            cr.set_source_rgba(c.red, c.green, c.blue, 1)
        cr.arc(w / 2, h / 2, min(w, h) / 2, 0, 2 * math.pi)
        cr.fill()


def _section(title: str) -> tuple[Gtk.Box, Gtk.Grid]:
    box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=8)
    label = Gtk.Label(xalign=0)
    label.set_markup(f"<b>{GLib.markup_escape_text(title)}</b>")
    box.append(label)
    frame = Gtk.Frame()
    grid = Gtk.Grid(column_spacing=16, row_spacing=10)
    for side in ("start", "end", "top", "bottom"):
        getattr(grid, f"set_margin_{side}")(14)
    frame.set_child(grid)
    box.append(frame)
    return box, grid


def _dim(text: str, wrap=True) -> Gtk.Label:
    lab = Gtk.Label(label=text, xalign=0, wrap=wrap)
    lab.add_css_class("dim-label")
    lab.set_wrap_mode(Pango.WrapMode.WORD_CHAR)
    lab.set_max_width_chars(60)
    return lab


class MainWindow(Gtk.ApplicationWindow):
    def __init__(self, app, config_path: str):
        super().__init__(application=app, title="Mouse Without Borders")
        self.config_path = config_path
        self.set_default_size(600, 860)
        self._loading = False
        self._loaded: dict[str, object] = {}
        self._unit = service.UnitState()
        self._poll_busy = False
        self._connected_remote = ""
        self._peer_screen = None  # (w, h) a connected Linux peer reported
        self._edge = "left"  # the two-machine side: where the other computer is
        self._waiting_for_settings = False  # mwb runs but has no usable settings
        self._layout_order: list[str] = []  # the screen order as the user arranged it
        self._layout_dirty = False
        self._grid_dirty: dict | None = None  # a 2D arrangement the user dragged, not yet saved
        self._group: dict | None = None  # a running group's layout, members and pointer
        self._status_addresses: list[str] = []  # this machine's, as mwb reported them

        scroller = Gtk.ScrolledWindow(hscrollbar_policy=Gtk.PolicyType.NEVER)
        outer = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        scroller.set_child(outer)
        scroller.set_vexpand(True)

        page = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=22)
        for side in ("start", "end", "top", "bottom"):
            getattr(page, f"set_margin_{side}")(20)
        outer.append(page)

        page.append(self._build_status())
        self.arrangement = ArrangementView(self._arrangement_changed, self._on_screen_selected,
                                           self._arrangement_grid_changed)
        arr_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=6)
        arr_box.append(self.arrangement)
        self.arrangement_hint = _dim("")
        self.arrangement_hint.set_justify(Gtk.Justification.CENTER)
        self.arrangement_hint.set_xalign(0.5)
        arr_box.append(self.arrangement_hint)
        # Appears when a screen is clicked: remove that computer from the group.
        self._forget_target = ""
        self._forgetting: set[str] = set()  # removed here, not yet confirmed gone
        self.forget_row = Gtk.Box(spacing=8, halign=Gtk.Align.CENTER)
        self.forget_label = _dim("")
        self.forget_row.append(self.forget_label)
        self.forget_btn = Gtk.Button()
        self.forget_btn.add_css_class("destructive-action")
        self.forget_btn.connect("clicked", lambda *_: self._confirm_forget(self._forget_target))
        self.forget_row.append(self.forget_btn)
        self.forget_row.set_visible(False)
        arr_box.append(self.forget_row)
        page.append(arr_box)
        page.append(self._build_connection())
        page.append(self._build_sharing())
        page.append(self._build_service())

        root = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        root.append(scroller)
        root.append(Gtk.Separator())
        root.append(self._build_actions())
        self.set_child(root)

        self.reload()
        self._poll()
        self._poll_source = GLib.timeout_add_seconds(STATUS_POLL_SECONDS, self._poll)
        self.connect("close-request", self._on_close)
        settings = Gtk.Settings.get_default()
        if settings is not None:
            for prop in ("gtk-theme-name", "gtk-application-prefer-dark-theme"):
                settings.connect(f"notify::{prop}", lambda *_: self._redraw_art())

    # --- building ---------------------------------------------------------

    def _build_status(self) -> Gtk.Widget:
        row = Gtk.Box(spacing=12)
        self.dot = StatusDot()
        row.append(self.dot)
        text = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2, hexpand=True)
        self.status_title = Gtk.Label(xalign=0, wrap=True)
        self.status_title.set_markup("<b>Checking…</b>")
        self.status_detail = _dim("")
        text.append(self.status_title)
        text.append(self.status_detail)
        row.append(text)
        self.btn_start = Gtk.Button(label="Start")
        self.btn_start.connect("clicked", lambda *_: self._service("start"))
        self.btn_restart = Gtk.Button(label="Restart")
        self.btn_restart.connect("clicked", lambda *_: self._service("restart"))
        self.btn_stop = Gtk.Button(label="Stop")
        self.btn_stop.connect("clicked", lambda *_: self._service("stop"))
        for b in (self.btn_start, self.btn_restart, self.btn_stop):
            b.set_valign(Gtk.Align.CENTER)
            row.append(b)
        return row

    def _field(self, grid, row, label_text, widget, help_text=None, label_for=None):
        lab = Gtk.Label(label=label_text, xalign=0)
        # Also gives screen readers the label for the control.
        lab.set_mnemonic_widget(label_for or widget)
        lab.set_valign(Gtk.Align.START)
        lab.set_margin_top(6)
        grid.attach(lab, 0, row, 1, 1)
        col = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4, hexpand=True)
        col.append(widget)
        hint = _dim(help_text or "")
        hint.set_visible(bool(help_text))
        col.append(hint)
        err = Gtk.Label(xalign=0, wrap=True)
        err.add_css_class("error")
        err.set_visible(False)
        col.append(err)
        grid.attach(col, 1, row, 1, 1)
        return hint, err

    def _build_connection(self) -> Gtk.Widget:
        box, grid = _section("Connection")
        self.peer = Gtk.DropDown.new_from_strings([label for _, label in PEERS])
        self.peer.connect("notify::selected", self._changed)
        self.peer_hint, self.peer_err = self._field(grid, 0, "Other computer", self.peer, PEER_HELP["windows"])

        self.host = Gtk.Entry(placeholder_text="192.168.1.100 or desk.local", hexpand=True)
        self.host.connect("changed", self._changed)
        self.host_hint, self.host_err = self._field(grid, 1, "Address", self.host)
        self._local_address = service.local_address()

        self._interfaces: list[service.Interface] = []
        self._interface_names: list[str] = []
        self.iface = Gtk.DropDown.new_from_strings(["All interfaces (recommended)"])
        self.iface.connect("notify::selected", self._changed)
        self.iface_hint, self.iface_err = self._field(
            grid, 2, "Network", self.iface,
            "Which network mwb uses to reach the other computers. Pick one to listen and connect only there.")

        key_row = Gtk.Box(spacing=6)
        self.key = Gtk.PasswordEntry(hexpand=True)
        self.key.set_show_peek_icon(True)
        self.key.connect("changed", self._changed)
        gen = Gtk.Button(label="Generate")
        gen.set_tooltip_text("Make a new random key. Enter the same key on the other computer.")
        gen.connect("clicked", self._generate_key)
        copy = Gtk.Button(label="Copy")
        copy.connect("clicked", self._copy_key)
        key_row.append(self.key)
        key_row.append(gen)
        key_row.append(copy)
        self.key_hint, self.key_err = self._field(grid, 3, "Security key", key_row,
                                                  "Both computers must use the same key.", label_for=self.key)

        self.name = Gtk.Entry(placeholder_text=socket.gethostname()[: cf.NAME_MAX], max_length=cf.NAME_MAX)
        self.name.connect("changed", self._changed)
        self.name_hint, self.name_err = self._field(grid, 4, "This computer’s name", self.name)

        size_row = Gtk.Box(spacing=6)
        self.rw = Gtk.SpinButton.new_with_range(200, 16384, 1)
        self.rh = Gtk.SpinButton.new_with_range(200, 16384, 1)
        for s in (self.rw, self.rh):
            s.set_numeric(True)
            s.connect("value-changed", self._changed)
        size_row.append(self.rw)
        size_row.append(Gtk.Label(label="×"))
        size_row.append(self.rh)
        size_row.append(_dim("pixels", wrap=False))
        self.size_hint, self.size_err = self._field(grid, 5, "Other screen size", size_row,
                                       "Used to place the pointer on the other screen.", label_for=self.rw)
        return box

    def _switch_row(self, grid, row, title, subtitle):
        text = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2, hexpand=True)
        text.append(Gtk.Label(label=title, xalign=0, wrap=True))
        sub = _dim(subtitle)
        text.append(sub)
        sw = Gtk.Switch(valign=Gtk.Align.CENTER)
        grid.attach(text, 0, row, 1, 1)
        grid.attach(sw, 1, row, 1, 1)
        try:
            sw.update_property([Gtk.AccessibleProperty.LABEL], [title])
        except (AttributeError, TypeError):
            pass
        return sw, sub

    def _build_sharing(self) -> Gtk.Widget:
        box, grid = _section("Sharing")
        self.bidi, self.bidi_sub = self._switch_row(
            grid, 0, "Control the other computer from here",
            "Move the pointer past the shared edge to use this keyboard and mouse there. "
            "When off, the other computer can still control this one.")
        self.clip, _ = self._switch_row(grid, 1, "Share the clipboard", "Copied text and images are available on both computers.")
        self.files, self.files_sub = self._switch_row(
            grid, 2, "Copy files", "Copy one file, then paste it on the other computer. Up to 100 MB.")
        for sw in (self.bidi, self.clip, self.files):
            sw.connect("notify::active", self._changed)
        self.bidi.connect("notify::active", self._bidi_toggled)
        return box

    def _build_service(self) -> Gtk.Widget:
        box, grid = _section("Background service")
        self.autostart, self.autostart_sub = self._switch_row(
            grid, 0, "Start when I log in", "Runs mwb as a user service, without administrator rights.")
        self.autostart.connect("state-set", self._autostart_set)
        self.override_note = Gtk.Label(xalign=0, wrap=True)
        self.override_note.add_css_class("warning")
        self.override_note.set_visible(False)
        grid.attach(self.override_note, 0, 1, 2, 1)

        self.log_expander = Gtk.Expander(label="Recent log")
        self.log_view = Gtk.TextView(editable=False, monospace=True, cursor_visible=False,
                                     wrap_mode=Gtk.WrapMode.WORD_CHAR)
        self.log_view.set_size_request(-1, 160)
        log_scroll = Gtk.ScrolledWindow(min_content_height=160)
        log_scroll.set_child(self.log_view)
        self.log_expander.set_child(log_scroll)
        self.log_expander.connect("notify::expanded", self._log_expanded)
        grid.attach(self.log_expander, 0, 2, 2, 1)

        clear_row = Gtk.Box(spacing=12)
        clear_text = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2, hexpand=True)
        clear_text.append(Gtk.Label(label="Start over", xalign=0))
        clear_text.append(_dim("Clears the security key, address, screen order and remembered computers. "
                               "A copy is kept as config.toml.bak."))
        clear_row.append(clear_text)
        self.btn_clear = Gtk.Button(label="Clear saved settings…")
        self.btn_clear.add_css_class("destructive-action")
        self.btn_clear.set_valign(Gtk.Align.CENTER)
        self.btn_clear.connect("clicked", self._confirm_clear)
        clear_row.append(self.btn_clear)
        grid.attach(clear_row, 0, 3, 2, 1)
        return box

    def _build_actions(self) -> Gtk.Widget:
        bar = Gtk.Box(spacing=8)
        for side in ("start", "end", "top", "bottom"):
            getattr(bar, f"set_margin_{side}")(12)
        # The running (or installed) client version, pinned to the far left of
        # the footer, beside the save message and the Revert/Save buttons.
        self.version_label = Gtk.Label(xalign=0)
        self.version_label.add_css_class("dim-label")
        self.version_label.set_tooltip_text("Installed mwb version")
        bar.append(self.version_label)
        self.save_msg = Gtk.Label(xalign=0, hexpand=True, wrap=True)
        self.save_msg.add_css_class("dim-label")
        bar.append(self.save_msg)
        self.btn_apply_restart = Gtk.Button(label="Restart now")
        self.btn_apply_restart.set_visible(False)
        self.btn_apply_restart.connect("clicked", lambda *_: self._service("restart"))
        bar.append(self.btn_apply_restart)
        self.btn_revert = Gtk.Button(label="Revert")
        self.btn_revert.connect("clicked", lambda *_: self.reload())
        bar.append(self.btn_revert)
        self.btn_save = Gtk.Button(label="Save")
        self.btn_save.add_css_class("suggested-action")
        self.btn_save.connect("clicked", self._save)
        bar.append(self.btn_save)
        return bar

    # --- form state --------------------------------------------------------

    def reload(self):
        self._loading = True
        doc = None
        try:
            try:
                doc = cf.load(self.config_path)
                values = doc.values()
                self.save_msg.set_text("" if doc.lines else "No settings yet. Fill in the connection, then save.")
                self._config_error = ""
            except (cf.ConfigError, OSError) as exc:
                values = dict(cf.DEFAULTS)
                self._config_error = f"The settings file can’t be edited here: {exc}. Fix {self.config_path} by hand."
                self.save_msg.set_text(self._config_error)
            self._loaded = values
            # Unset in the file means "the default for this peer type", which
            # should follow the peer choice until the user flips the switch.
            self._bidi_follows_peer = doc is None or "bidirectional" not in doc.entries
            self._last_peer = values["peer"]
            self.peer.set_selected(0 if values["peer"] != "linux" else 1)
            self.host.set_text(str(values["host"]))
            self.key.set_text(str(values["key"]))
            self.name.set_text(str(values["name"]))
            self.rw.set_value(int(values["remote_width"]))
            self.rh.set_value(int(values["remote_height"]))
            self.bidi.set_active(bool(values["bidirectional"]))
            self.clip.set_active(bool(values["clipboard"]))
            self.files.set_active(bool(values["file_transfer"]))
            self._edge = str(values["edge"])
            self._layout_order, self._layout_dirty = [], False
            self._grid_dirty = None
            self._refresh_interfaces(str(values["interface"]))
            self.btn_apply_restart.set_visible(False)
            if doc is not None and not doc.lines:
                # First run: a new key, another Linux computer, and wait for
                # it to connect. One Save and this computer is listening.
                self.peer.set_selected(1)
                self._last_peer = "linux"
                self.bidi.set_active(True)
                self.key.set_text(generate_key())
                self.save_msg.set_text("Not set up yet. Check the settings, copy the security key to the "
                                       "other computer, then click Save.")
        finally:
            self._loading = False
        self._changed()

    def form_values(self) -> dict[str, object]:
        return {
            "peer": PEERS[self.peer.get_selected()][0] if self.peer.get_selected() < len(PEERS) else "windows",
            "host": self.host.get_text().strip(),
            "key": self.key.get_text(),
            "name": self.name.get_text().strip(),
            "edge": self._edge,
            "interface": self._selected_interface(),
            "layout": self._layout_to_save(),
            "grid": self._grid_to_save(),
            "bidirectional": self.bidi.get_active(),
            "clipboard": self.clip.get_active(),
            "file_transfer": self.files.get_active(),
            "remote_width": int(self.rw.get_value()),
            "remote_height": int(self.rh.get_value()),
        }

    def _layout_to_save(self) -> list:
        """The screen order to write, with forgotten machines removed so a save
        can never put one back into the config the client reads on restart."""
        order = list(self._layout_order) if self._layout_dirty else list(self._loaded.get("layout") or [])
        hidden = self._hidden()
        return [n for n in order if n not in hidden] if hidden else order

    def _loaded_grid_cells(self) -> dict:
        """The grid saved in the config, parsed to {name: (col, row)}."""
        cells = {}
        for e in (self._loaded.get("grid") or []):
            parts = str(e).split(":")
            if len(parts) == 3:
                try:
                    cells[parts[0]] = (int(parts[1]), int(parts[2]))
                except ValueError:
                    pass
        return cells

    def _grid_to_save(self) -> list:
        """The 2D arrangement to write as ["name:col:row", …], forgotten
        machines removed. Empty when the arrangement is a plain row (then the
        row in layout is enough), so it degrades cleanly."""
        cells = self._grid_dirty if self._grid_dirty is not None else self._loaded_grid_cells()
        hidden = self._hidden()
        cells = {n: p for n, p in cells.items() if n not in hidden}
        if not cells or max((r for _, r in cells.values()), default=0) == 0:
            return []
        return [f"{n}:{c}:{r}" for n, (c, r) in sorted(cells.items())]

    def changed_values(self) -> dict[str, object]:
        """Only the settings the user changed, so a save never overwrites an
        edit someone made to another key in the file meanwhile."""
        return {k: v for k, v in self.form_values().items() if self._loaded.get(k) != v}

    def _refresh_interfaces(self, wanted: str | None = None):
        """Fill the Network list; keep the current choice, even if that
        interface is gone right now (a cable unplugged)."""
        if wanted is None:
            wanted = self._selected_interface()
        found = service.list_interfaces()
        shown = [i for i in found if not i.hidden or i.name == wanted]
        if wanted and wanted not in [i.name for i in shown]:
            shown.append(service.Interface(name=wanted, kind="Missing", up=False, addresses=[]))
        names = [i.name for i in shown]
        labels = [i.label() for i in shown]
        self._interfaces = shown
        if names == self._interface_names and getattr(self, "_iface_labels", None) == labels:
            return
        self._interface_names, self._iface_labels = names, labels
        was_loading, self._loading = self._loading, True
        try:
            self.iface.set_model(Gtk.StringList.new(["All interfaces (recommended)"] + labels))
            self.iface.set_selected(names.index(wanted) + 1 if wanted in names else 0)
        finally:
            self._loading = was_loading

    def _selected_interface(self) -> str:
        i = self.iface.get_selected()
        if 1 <= i <= len(self._interface_names):
            return self._interface_names[i - 1]
        return ""

    def _here_address(self) -> str:
        """The address the other computers should enter for this one."""
        chosen = self._selected_interface()
        for i in self._interfaces:
            if i.name == chosen and i.address:
                return i.address
        if chosen:
            return ""
        return self._local_address or (self._status_addresses[0] if self._status_addresses else "")

    def _arrangement_changed(self, order: list[str]):
        if OTHER in order or ME in order:
            # Two machines without names in a layout yet: the side changed.
            me = self.arrangement.me
            self._edge = "left" if order.index(me) == 1 else "right"
        else:
            self._layout_order, self._layout_dirty = list(order), True
            self._grid_dirty = None  # a plain-row edit clears any 2D arrangement
        self._changed()

    def _arrangement_grid_changed(self, cells: dict):
        """The user dragged a screen in the 2D arrangement."""
        self._grid_dirty = {str(n): (int(c), int(r)) for n, (c, r) in cells.items()}
        # Keep the row projection (top-to-bottom, left-to-right) in step.
        self._layout_order = sorted(self._grid_dirty, key=lambda n: (self._grid_dirty[n][1], self._grid_dirty[n][0]))
        self._layout_dirty = True
        self._changed()

    def _me_name(self, v: dict) -> str:
        return str(v["name"] or socket.gethostname()[: cf.NAME_MAX])

    def _hidden(self) -> set:
        """Machines that must never appear in the arrangement or be saved: the
        tombstones the client holds, plus any just forgotten here and not yet
        written. Reading the store makes this survive a stale config, a save or
        a Start/Stop — the forgotten machine stays gone. A machine the group
        reports online now is never hidden, so a live machine always shows even
        if a stale tombstone lingers while the group converges."""
        hidden = self._forgetting | service.forgotten_names()
        online = {str(m.get("name") or "") for m in (self._group or {}).get("members") or [] if m.get("online")}
        return hidden - online

    def _display_layout(self, me: str) -> list[str]:
        """The screen order to show for a Linux computer: the user's edit, the
        running group's, the saved one, or the two-machine side. Forgotten
        machines are held out, so one cannot flicker back from a stale source."""
        if self._layout_dirty and self._layout_order:
            order = self._layout_order
        else:
            live = [str(n) for n in ((self._group or {}).get("layout") or [])]
            saved = [str(n) for n in (self._loaded.get("layout") or [])]
            if len(live) >= 2 and me in live:
                order = live
            elif len(saved) >= 2 and me in saved:
                order = saved
            else:
                other = OTHER
                members = (self._group or {}).get("members") or []
                if len(members) == 1 and members[0].get("name"):
                    other = str(members[0]["name"])
                order = [other, me] if self._edge == "left" else [me, other]
        hidden = self._hidden()
        if hidden:
            order = [n for n in order if n not in hidden]
            if me not in order:
                order.append(me)
        return order

    def _display_cells(self, me: str, hidden: set) -> dict:
        """The 2D arrangement to draw, as {name: (col, row)}: the user's own
        drag in progress, else the running group's positions. Empty when there
        is no 2D arrangement (a plain row), so the caller falls back to the row
        view. Forgotten machines are left out; this machine is always included."""
        if self._grid_dirty is not None:
            cells = {n: p for n, p in self._grid_dirty.items() if n not in hidden}
            return cells if me in cells else {}
        pos = (self._group or {}).get("pos") or {}
        if pos and me in pos:
            cells = {}
            for name, p in pos.items():
                name = str(name)
                if name in hidden:
                    continue
                try:
                    cells[name] = (int(p["c"]), int(p["r"]))
                except (KeyError, TypeError, ValueError):
                    return {}
            if me in cells:
                return cells
        # No live positions (e.g. the service is stopped): use the grid saved in
        # the config so a stopped service shows the real 2D arrangement instead
        # of flattening it to a row.
        saved = {n: p for n, p in self._loaded_grid_cells().items() if n not in hidden}
        if me in saved and max((r for _, r in saved.values()), default=0) > 0:
            return saved
        # Otherwise lay the row out as a single-row grid, so the drag works from
        # a plain row too — for a real named group only (no ME / OTHER).
        order = self._display_layout(me)
        if len(order) >= 2 and me in order and OTHER not in order and ME not in order:
            return {n: (i, 0) for i, n in enumerate(order)}
        return {}

    def _sync_arrangement(self, v: dict):
        a = self.arrangement
        me = self._me_name(v)
        local_aspect = self._local_aspect()
        other_title = self._connected_remote or str(v["host"]) or "Other computer"
        if self._peer_screen:
            pw, ph = self._peer_screen
            other_aspect = pw / ph
        elif v["remote_height"]:
            other_aspect = int(v["remote_width"]) / int(v["remote_height"])
        else:
            other_aspect = 16 / 9
        if v["peer"] != "linux":
            a.titles = {ME: me, OTHER: other_title}
            a.kinds = {OTHER: "Windows PC"}
            a.aspects = {ME: local_aspect, OTHER: other_aspect}
            a.offline, a.location = set(), ""
            a.set_order([OTHER, ME] if self._edge == "left" else [ME, OTHER], ME)
        else:
            hidden = self._hidden()
            # Once a tombstone is recorded, the transient copy is redundant.
            self._forgetting -= service.forgotten_names()
            a.titles = {me: me, OTHER: other_title}
            a.kinds = {}
            a.aspects = {me: local_aspect, OTHER: other_aspect}
            a.offline = set()
            for m in (self._group or {}).get("members") or []:
                name = str(m.get("name") or "")
                if name in hidden:
                    continue
                if m.get("width") and m.get("height"):
                    a.aspects[name] = int(m["width"]) / int(m["height"])
                if not m.get("online"):
                    a.offline.add(name)
            a.location = str((self._group or {}).get("location") or "")
            cells = self._display_cells(me, hidden)
            if cells:
                a.set_grid(cells, me)
            else:
                a.set_order(self._display_layout(me), me)
        a.refresh()
        self._refresh_forget_row()
        self._update_arrangement_hint()

    def _is_linux_group(self) -> bool:
        return self.arrangement.me not in (ME, OTHER)

    def _forgettable(self, name: str) -> bool:
        if name in (ME, OTHER) or name in self._hidden():
            return False
        return service.forgettable(name, self.arrangement.me, self.arrangement.offline,
                                   self._unit.running, self._is_linux_group())

    def _on_screen_selected(self, name: str):
        self._refresh_forget_row(name)

    def _refresh_forget_row(self, name: str | None = None):
        name = self.arrangement.selected if name is None else name
        if self._forgettable(name):
            self._forget_target = name
            self.forget_label.set_markup(f"<b>{GLib.markup_escape_text(name)}</b> is selected")
            self.forget_btn.set_label(f"Forget {name}")
            self.forget_btn.set_tooltip_text(f"Remove {name} from the group on every computer")
            self.forget_row.set_visible(True)
        else:
            self._forget_target = ""
            self.forget_row.set_visible(False)

    def _update_arrangement_hint(self):
        a = self.arrangement
        if a._is_grid():
            self.arrangement_hint.set_text(
                "The screens as they sit on your desk. Click one to select it, then use the arrow keys "
                "to move it up, down, left or right — or drag it beside another. The pointer crosses where "
                "screens touch. Offline computers are skipped.")
            return
        if len(a.order) > 2:
            self.arrangement_hint.set_text(
                "The screens from left to right, as they stand on your desk. Drag a screen to move it; "
                "the pointer crosses where screens touch. Offline computers are skipped.")
            return
        other = next((a.title(n) for n in a.order if n != a.me), "The other computer")
        side = "left" if a.order and a.order[0] != a.me else "right"
        self.arrangement_hint.set_text(
            f"{other} is to the {side} of this computer. "
            "Click the screens to swap them. The highlighted edge is where the pointer crosses.")

    def _bidi_toggled(self, *_):
        if not self._loading:
            self._bidi_follows_peer = False

    def _follow_peer_default(self, peer: str):
        """Keep an unset bidirectional at its default when the peer changes."""
        if peer == getattr(self, "_last_peer", peer):
            return
        self._last_peer = peer
        if getattr(self, "_bidi_follows_peer", False):
            self._loading = True
            try:
                self.bidi.set_active(peer == "linux")
            finally:
                self._loading = False

    def _changed(self, *_):
        if self._loading:
            return
        self._follow_peer_default(PEERS[self.peer.get_selected()][0]
                                  if self.peer.get_selected() < len(PEERS) else "windows")
        v = self.form_values()
        peer = str(v["peer"])
        self.peer_hint.set_text(PEER_HELP[peer])
        if self._peer_screen:
            pw, ph = self._peer_screen
            self.size_hint.set_text(f"{self._connected_remote or 'The other computer'} reports {pw} × {ph}. "
                                    "mwb uses that automatically; this value is only a fallback.")
        else:
            self.size_hint.set_text("Used to place the pointer on the other screen.")
        self._sync_arrangement(v)
        self.files.set_sensitive(bool(v["clipboard"]))
        self.files_sub.set_text(
            "Copy one file, then paste it on the other computer. Up to 100 MB."
            if v["clipboard"] else "Needs clipboard sharing, which starts each copy.")
        # Like pairing PowerToys machines: one waits, the other connects.
        if peer == "linux":
            self.host.set_placeholder_text("Leave empty to wait for the other computer")
            here = self._here_address()
            where = (f"this computer’s address, {here}" if here
                     else "this computer’s address (run hostname -I to see it)")
            self.host_hint.set_text(
                f"Leave it empty on one computer, and on the other enter {where}."
                if v["host"] else
                f"This computer will wait. On the other computer, enter {where}.")
        else:
            self.host.set_placeholder_text("192.168.1.100 or desk.local")
            self.host_hint.set_text("")
        self.host_hint.set_visible(peer == "linux")
        self.name_hint.set_visible(peer == "linux")
        self.name_hint.set_text("Must be different from the other computer’s name.")
        self.bidi_sub.set_text("Move the pointer past the shared edge to use this keyboard and mouse there. "
                               "When off, the other computer can still control this one.")

        problems = cf.validate(v)
        advice = cf.warnings(v)
        for key, err in (("peer", self.peer_err), ("host", self.host_err), ("key", self.key_err),
                         ("name", self.name_err), ("remote_width", self.size_err)):
            msg = problems.get(key) or (problems.get("remote_height") if key == "remote_width" else None)
            soft = advice.get(key) if not msg else None
            err.set_text(msg or soft or "")
            err.set_visible(bool(msg or soft))
            # Advice reads as helper text; only blocking problems use the error style.
            (err.remove_css_class if soft else err.add_css_class)("error")
            (err.add_css_class if soft else err.remove_css_class)("dim-label")
        dirty = bool(self.changed_values()) or not os.path.exists(self.config_path)
        can_save = dirty and not problems and not getattr(self, "_config_error", "")
        self.btn_save.set_sensitive(can_save)
        self.btn_revert.set_sensitive(bool(self.changed_values()))
        if dirty and self.btn_apply_restart.get_visible():
            self.btn_apply_restart.set_visible(False)

    def _local_aspect(self) -> float:
        display = Gdk.Display.get_default()
        try:
            monitors = display.get_monitors()
            mon = monitors.get_item(0) if monitors.get_n_items() else None
            if mon is not None:
                g = mon.get_geometry()
                if g.height > 0:
                    return g.width / g.height
        except (AttributeError, TypeError):
            pass
        return 16 / 9

    def _redraw_art(self):
        self.arrangement.queue_draw()
        self.dot.queue_draw()

    # --- actions -----------------------------------------------------------

    def _generate_key(self, *_):
        self.key.set_text(generate_key())
        self.key.set_show_peek_icon(True)
        self.save_msg.set_text("New key generated. Enter the same key on the other computer, then save.")

    def _copy_key(self, *_):
        text = self.key.get_text()
        if not text:
            return
        clipboard = self.get_clipboard()
        try:
            clipboard.set(text)
        except TypeError:
            clipboard.set_content(Gdk.ContentProvider.new_for_value(text))
        self.save_msg.set_text("Security key copied.")

    def _save(self, *_):
        updates = self.changed_values()
        first_run = not os.path.exists(self.config_path)
        if first_run:
            # A brand-new file needs the required settings even if unchanged.
            fv = self.form_values()
            # bidirectional is left out: unset means the peer type's default.
            for k in ("host", "key", "peer", "edge"):
                updates.setdefault(k, fv[k])
        if not updates.get("name", True):
            updates.pop("name")  # empty means "use the hostname"; don't write ""
        if "edge" in updates:
            updates["edge_changed"] = int(time.time())
        if "layout" in updates or "grid" in updates:
            # In a group the most recently chosen arrangement wins, and mwb
            # copies it to the other computers.
            updates["layout_changed"] = int(time.time())
        try:
            cf.save(updates, self.config_path)
        except (cf.ConfigError, OSError) as exc:
            self.save_msg.set_text(f"Not saved: {exc}")
            return
        self.reload()
        if self._unit.running and self._waiting_for_settings:
            # mwb is running and waiting for exactly this file: it starts now.
            self.save_msg.set_text("Saved. mwb is starting with these settings.")
        elif self._unit.running:
            self.save_msg.set_text("Saved. Restart the service to use the new settings.")
            self.btn_apply_restart.set_visible(True)
        elif first_run and self._unit.installed:
            self.save_msg.set_text("Saved. Starting mwb…")
            self._service("start")
        else:
            self.save_msg.set_text("Saved.")

    def _confirm_forget(self, name: str):
        if not name:
            return
        title = f"Forget {name}?"
        body = (f"{name} is removed from this group on every computer. If you later turn it back on, "
                "it rejoins on its own.")
        self._confirm(title, body, "Forget", lambda: self._forget(name))

    def _forget(self, name: str):
        try:
            err = service.request_forget(name)
        except ValueError:
            self.save_msg.set_text(f"Couldn’t forget {name}: not a valid computer name.")
            return
        if err:
            self.save_msg.set_text(f"Couldn’t forget {name}: {err}")
            return
        # Drop it from the picture now; keep holding it out until the service
        # confirms it is gone, so it does not flicker back during the restart.
        self._forgetting.add(name)
        self.arrangement.remove_screen(name)
        self._refresh_forget_row()
        if self._unit.running:
            self._service("restart", done_msg=f"{name} removed from the group.")
        else:
            self.save_msg.set_text(f"{name} will be removed when mwb next starts.")

    def _confirm(self, title: str, body: str, action_label: str, on_confirm):
        """Ask a yes/no with a destructive action, on GTK 4.10+ or older."""
        if hasattr(Gtk, "AlertDialog"):  # GTK 4.10+
            dialog = Gtk.AlertDialog(message=title, detail=body, buttons=["Cancel", action_label],
                                     cancel_button=0, default_button=0)

            def done(d, result):
                try:
                    if d.choose_finish(result) == 1:
                        on_confirm()
                except GLib.Error:
                    pass  # dismissed

            dialog.choose(self, None, done)
            return
        dialog = Gtk.MessageDialog(transient_for=self, modal=True, message_type=Gtk.MessageType.WARNING,
                                   buttons=Gtk.ButtonsType.NONE, text=title, secondary_text=body)
        dialog.add_button("Cancel", Gtk.ResponseType.CANCEL)
        go = dialog.add_button(action_label, Gtk.ResponseType.ACCEPT)
        go.add_css_class("destructive-action")
        dialog.set_default_response(Gtk.ResponseType.CANCEL)

        def respond(d, response):
            d.destroy()
            if response == Gtk.ResponseType.ACCEPT:
                on_confirm()

        dialog.connect("response", respond)
        dialog.present()

    def _confirm_clear(self, *_):
        title = "Clear the saved settings?"
        body = ("This computer forgets its security key, the other computers’ addresses and the screen order, "
                "and stops sharing until you set it up again. The old settings are kept as "
                f"{os.path.basename(self.config_path)}.bak.")
        self._confirm(title, body, "Clear settings", self._clear_settings)

    def _clear_settings(self):
        try:
            backup = cf.clear(self.config_path)
        except OSError as exc:
            self.save_msg.set_text(f"Couldn’t clear the settings: {exc}")
            return
        self.reload()  # back to first-run setup
        note = f" The old ones are in {backup}." if backup else ""
        if self._unit.running:
            # mwb keeps the old settings in memory until it restarts; it then
            # waits for new ones.
            self._service("restart", "Settings cleared; mwb is waiting for new ones." + note)
        else:
            self.save_msg.set_text("Settings cleared." + note)

    def _service(self, action, done_msg: str | None = None):
        self.save_msg.set_text({"start": "Starting…", "stop": "Stopping…", "restart": "Restarting…"}.get(action, ""))

        def work():
            err = service.control(action)
            GLib.idle_add(done, err)

        def done(err):
            if err:
                self.save_msg.set_text(f"Couldn’t {action} the service: {err}")
            else:
                self.save_msg.set_text(done_msg or {"start": "Started.", "stop": "Stopped.", "restart": "Restarted."}[action])
                self.btn_apply_restart.set_visible(False)
            self._poll()
            return False

        threading.Thread(target=work, daemon=True).start()

    def _autostart_set(self, _sw, state):
        if self._loading:
            return False

        def work():
            err = service.control("enable" if state else "disable")
            GLib.idle_add(done, err)

        def done(err):
            if err:
                self.save_msg.set_text(f"Couldn’t change start at login: {err}")
            self._poll()
            return False

        threading.Thread(target=work, daemon=True).start()
        return False

    def _log_expanded(self, exp, _pspec):
        if not exp.get_expanded():
            return

        def work():
            text = service.recent_log()
            GLib.idle_add(lambda: self.log_view.get_buffer().set_text(text) or False)

        threading.Thread(target=work, daemon=True).start()

    # --- status ------------------------------------------------------------

    def _poll(self):
        if self._poll_busy:
            return True
        self._poll_busy = True

        def work():
            unit = service.query()
            st = service.read_status()
            problem = service.uinput_problem()
            GLib.idle_add(self._show_status, unit, st, problem)

        threading.Thread(target=work, daemon=True).start()
        return True

    def _show_status(self, unit: service.UnitState, st: dict | None, problem=None):
        self._poll_busy = False
        self._unit = unit
        # The running client reports its own version; fall back to the installed
        # binary when it is stopped.
        ver = (str(st.get("version")) if st and st.get("version") else "") or service.client_version()
        self.version_label.set_text(f"mwb {ver}" if ver else "")
        connected = False
        state = str(st.get("state") or "") if st else ""
        self._waiting_for_settings = state in ("not_configured", "config_error")
        if state == "not_configured":
            kind, title = "wait", "Not set up yet"
            detail = ("mwb is running and waiting for its settings. Check them below and click Save; "
                      "it starts as soon as they are saved.")
        elif state == "config_error":
            kind, title = "error", "The settings can’t be used"
            detail = f"{st.get('error') or 'The settings file has a problem.'} Fix it below and save; mwb retries by itself."
        elif state == "no_network":
            kind, title = "wait", f"Waiting for the network {st.get('interface') or ''}".rstrip()
            detail = (f"{st.get('error') or 'The chosen network has no address yet.'} "
                      "mwb starts once it is connected, or choose another network below.")
        elif st and st.get("state") == "connected" and st.get("group"):
            kind, title, detail = service.describe_group(st)
            connected = True
        elif st and st.get("state") == "connected":
            kind, title, detail = service.describe_connection(st)
            connected = True
        elif st and st.get("state") == "connecting" and st.get("waiting"):
            addrs = ", ".join(st.get("addresses") or []) or self._local_address or "this computer’s address"
            kind, title = "wait", "Waiting for the other computer"
            detail = f"On the other computer, enter {addrs} as the address, with the same security key."
        elif st and st.get("state") == "connecting":
            kind, title = "wait", f"Looking for {st.get('host')}…"
            detail = "Make sure the other computer is on and using the same security key."
        elif problem and not unit.running:
            # The exact cause and fix, ahead of the generic service states.
            kind, (title, detail) = "error", problem
        elif unit.crash_looping:
            kind, title = "error", "Keeps stopping"
            detail = "mwb exits with an error and systemd keeps restarting it. Open Recent log below to see why."
        elif unit.running:
            kind, title, detail = "ok", "Running", "Waiting for connection details from mwb."
        elif unit.error:
            kind, title = "off", "Can’t check the background service"
            detail = f"systemctl reported: {unit.error}"
        elif not unit.installed:
            kind, title = "off", "Not set up to run in the background"
            detail = "Install the mwb service, or run mwb from a terminal."
        elif unit.active == "failed":
            kind, title, detail = "error", "Stopped after an error", "Open Recent log below to see why."
        else:
            kind, title, detail = "off", "Not running", "Start mwb to connect to the other computer."
        # A computer reaching this one with the wrong security key looks exactly
        # like "not connected", so mwb records it and we say so plainly here,
        # ahead of the generic connecting/waiting message, while it is current.
        if not connected:
            mismatch_from = service.recent_key_mismatch(st)
            if mismatch_from:
                kind, title = "error", "Different security key"
                detail = (f"A computer at {mismatch_from} is trying to connect with a different "
                          "security key. Set the same key on both computers, then they connect.")
        self.dot.set_kind(kind)
        self.status_title.set_markup(f"<b>{GLib.markup_escape_text(title)}</b>")
        self.status_detail.set_text(detail)
        self.status_detail.set_visible(bool(detail))
        self._polls = getattr(self, "_polls", 0) + 1
        if self._polls % 5 == 0:
            self._refresh_interfaces()  # a cable plugged in, Wi-Fi joined
        addresses = [str(a) for a in (st.get("addresses") or [])] if st else []
        if addresses != self._status_addresses:
            self._status_addresses = addresses
            self._changed()
        remote = str(st.get("remote") or "") if connected and st else ""
        pw = int(st.get("peer_screen_width") or 0) if connected and st else 0
        ph = int(st.get("peer_screen_height") or 0) if connected and st else 0
        group = None
        if st and st.get("group"):
            group = {k: st.get(k) for k in ("layout", "members", "location", "pos")}
            online = [m for m in (st.get("members") or []) if m.get("online")]
            if len(online) == 1 and online[0].get("width") and online[0].get("height"):
                pw, ph = int(online[0]["width"]), int(online[0]["height"])
        peer_screen = (pw, ph) if pw > 0 and ph > 0 else None
        if (self.arrangement.connected != connected or self._connected_remote != remote
                or self._peer_screen != peer_screen or self._group != group):
            self.arrangement.connected = connected
            self._connected_remote = remote
            self._peer_screen = peer_screen
            self._group = group
            self._changed()  # redraws the arrangement and the size hint

        # mwb saved the mirror of a side chosen more recently on the peer.
        adopted_from = str(st.get("edge_from") or "") if st else ""
        adopted = (adopted_from, st.get("edge")) if adopted_from and st else None
        if (adopted and adopted != getattr(self, "_adoption_seen", None) and adopted[1] in ("left", "right")
                and adopted[1] != self._loaded.get("edge") and "edge" not in self.changed_values()):
            self._adoption_seen = adopted
            self.reload()
            self.save_msg.set_text(f"Screen arrangement updated to match {adopted_from}, where it was changed more recently.")

        # A crash loop is still a started unit: offer Stop to end it.
        started = unit.running or unit.crash_looping
        self.btn_start.set_visible(unit.installed and not started)
        self.btn_stop.set_visible(unit.installed and started)
        self.btn_restart.set_visible(unit.installed and started)
        self.autostart.set_sensitive(unit.installed)
        self._loading = True
        self.autostart.set_active(unit.enabled)
        self._loading = False

        flags = unit.overriding_flags()
        if flags:
            self.override_note.set_text(
                "The service starts mwb with " + " ".join(flags) +
                ", which overrides the matching settings above. Remove those options from "
                "the service to manage them here.")
        self.override_note.set_visible(bool(flags))
        return False

    def _on_close(self, *_):
        if getattr(self, "_poll_source", None):
            GLib.source_remove(self._poll_source)
            self._poll_source = None
        return False


class MwbApp(Gtk.Application):
    def __init__(self, config_path: str, background: bool = False):
        super().__init__(application_id=APP_ID, flags=Gio.ApplicationFlags.DEFAULT_FLAGS
                         if hasattr(Gio.ApplicationFlags, "DEFAULT_FLAGS") else Gio.ApplicationFlags.FLAGS_NONE)
        self.config_path = config_path
        self.background = background  # started at login: only the tray icon
        self.tray = None

    def do_startup(self):
        Gtk.Application.do_startup(self)
        if not tray.available():
            return  # no tray host: closing the window quits, as before
        try:
            self.tray = tray.TrayIcon(self._tray_open, self._tray_toggle, self._tray_quit)
        except GLib.Error as exc:
            print(f"mwb-gui: no tray icon: {exc.message}", file=sys.stderr)
            return
        # The icon keeps the app alive with its window closed.
        self.hold()
        self._tray_poll()
        GLib.timeout_add_seconds(STATUS_POLL_SECONDS, self._tray_poll)

    def do_activate(self):
        if self.background:
            self.background = False  # later activations, from the icon or the launcher, open the window
            if self.tray is not None:
                return
        win = self.get_active_window()
        if win is None:
            win = MainWindow(self, self.config_path)
        win.present()

    def do_shutdown(self):
        if self.tray is not None:
            self.tray.close()
            self.tray = None
        Gtk.Application.do_shutdown(self)

    # --- tray -------------------------------------------------------------

    def _tray_poll(self):
        if self.tray is None:
            return False
        self.tray.update(*tray.tray_status(service.read_status()))
        return True

    def _tray_open(self):
        self.activate()

    def _tray_toggle(self, start: bool):
        def work():
            service.control("start" if start else "stop")
            GLib.idle_add(self._tray_poll)

        threading.Thread(target=work, daemon=True).start()

    def _tray_quit(self):
        # Quit means everything: stop sharing too, not just hide the icon.
        def work():
            service.control("stop")
            GLib.idle_add(self.quit)

        for win in self.get_windows():
            win.close()
        threading.Thread(target=work, daemon=True).start()


def main(argv=None) -> int:
    argv = list(sys.argv if argv is None else argv)
    config_path = cf.DEFAULT_PATH
    if "--version" in argv:
        print("mwb-gui", __version__)
        return 0
    background = "--background" in argv
    if background:
        argv.remove("--background")
    if "--config" in argv:
        i = argv.index("--config")
        if i + 1 >= len(argv):
            print("usage: mwb-gui [--config PATH] [--background]", file=sys.stderr)
            return 2
        config_path = os.path.abspath(os.path.expanduser(argv[i + 1]))
        del argv[i : i + 2]
    if background and not os.path.exists(config_path):
        return 0  # started at login, but mwb was never set up: no icon
    return MwbApp(config_path, background).run(argv[:1])
