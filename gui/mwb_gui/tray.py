"""System tray icon for mwb-gui.

GTK 4 has no tray API, and the AppIndicator libraries need GTK 3, which
cannot share a process with GTK 4. So the icon speaks the StatusNotifierItem
protocol (and the com.canonical.dbusmenu protocol for its menu) over D-Bus
directly, with Gio only. GNOME shows these through the AppIndicator
extension, which Ubuntu, Zorin and others enable by default; KDE and most
other desktops show them natively.

Left click opens the window; right click shows a menu with the connection
state, Open, Start or Stop, and Quit, which also stops mwb.
"""

from __future__ import annotations

import os
from typing import Callable

from gi.repository import Gio, GLib

from . import service

WATCHER = "org.kde.StatusNotifierWatcher"
WATCHER_PATH = "/StatusNotifierWatcher"
ITEM_PATH = "/StatusNotifierItem"
MENU_PATH = "/MenuBar"

ICON = "input-mouse"

_ITEM_XML = """
<node>
  <interface name="org.kde.StatusNotifierItem">
    <property name="Category" type="s" access="read"/>
    <property name="Id" type="s" access="read"/>
    <property name="Title" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="IconName" type="s" access="read"/>
    <property name="IconThemePath" type="s" access="read"/>
    <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
    <property name="ItemIsMenu" type="b" access="read"/>
    <property name="Menu" type="o" access="read"/>
    <method name="Activate"><arg type="i" direction="in"/><arg type="i" direction="in"/></method>
    <method name="SecondaryActivate"><arg type="i" direction="in"/><arg type="i" direction="in"/></method>
    <method name="ContextMenu"><arg type="i" direction="in"/><arg type="i" direction="in"/></method>
    <method name="Scroll"><arg type="i" direction="in"/><arg type="s" direction="in"/></method>
    <signal name="NewTitle"/>
    <signal name="NewIcon"/>
    <signal name="NewToolTip"/>
    <signal name="NewStatus"><arg type="s"/></signal>
  </interface>
</node>
"""

_MENU_XML = """
<node>
  <interface name="com.canonical.dbusmenu">
    <property name="Version" type="u" access="read"/>
    <property name="TextDirection" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="IconThemePath" type="as" access="read"/>
    <method name="GetLayout">
      <arg type="i" direction="in"/><arg type="i" direction="in"/><arg type="as" direction="in"/>
      <arg type="u" direction="out"/><arg type="(ia{sv}av)" direction="out"/>
    </method>
    <method name="GetGroupProperties">
      <arg type="ai" direction="in"/><arg type="as" direction="in"/>
      <arg type="a(ia{sv})" direction="out"/>
    </method>
    <method name="GetProperty">
      <arg type="i" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="out"/>
    </method>
    <method name="Event">
      <arg type="i" direction="in"/><arg type="s" direction="in"/>
      <arg type="v" direction="in"/><arg type="u" direction="in"/>
    </method>
    <method name="EventGroup">
      <arg type="a(isvu)" direction="in"/><arg type="ai" direction="out"/>
    </method>
    <method name="AboutToShow">
      <arg type="i" direction="in"/><arg type="b" direction="out"/>
    </method>
    <method name="AboutToShowGroup">
      <arg type="ai" direction="in"/><arg type="ai" direction="out"/><arg type="ai" direction="out"/>
    </method>
    <signal name="ItemsPropertiesUpdated">
      <arg type="a(ia{sv})"/><arg type="a(ias)"/>
    </signal>
    <signal name="LayoutUpdated"><arg type="u"/><arg type="i"/></signal>
    <signal name="ItemActivationRequested"><arg type="i"/><arg type="u"/></signal>
  </interface>
</node>
"""

# Menu item ids. 0 is the root.
ID_STATUS, ID_SEP1, ID_OPEN, ID_TOGGLE, ID_SEP2, ID_QUIT = 1, 2, 3, 4, 5, 6


class MenuModel:
    """The tray menu's items, kept apart from D-Bus so it can be tested."""

    def __init__(self):
        self.status = "Checking…"
        self.running = False

    def items(self) -> list[tuple[int, dict]]:
        """(id, properties) in display order; values are plain Python."""
        return [
            (ID_STATUS, {"label": self.status, "enabled": False}),
            (ID_SEP1, {"type": "separator"}),
            (ID_OPEN, {"label": "Open Mouse Without Borders"}),
            (ID_TOGGLE, {"label": "Stop sharing" if self.running else "Start sharing"}),
            (ID_SEP2, {"type": "separator"}),
            (ID_QUIT, {"label": "Quit"}),
        ]


def tray_status(st: dict | None) -> tuple[str, bool]:
    """(menu text, running) for the icon, from mwb's status file."""
    if st is None:
        return "Not running", False
    if st.get("state") == "connected":
        describe = service.describe_group if st.get("group") else service.describe_connection
        return describe(st)[1], True
    if st.get("waiting"):
        return "Waiting for the other computer", True
    return f"Connecting to {st.get('host') or 'the other computer'}…", True


def _props(p: dict) -> dict:
    out = {}
    for k, v in p.items():
        if isinstance(v, bool):
            out[k] = GLib.Variant("b", v)
        else:
            out[k] = GLib.Variant("s", v)
    return out


def available(bus: Gio.DBusConnection | None = None) -> bool:
    """Whether a tray host is running on the session bus."""
    try:
        bus = bus or Gio.bus_get_sync(Gio.BusType.SESSION, None)
        r = bus.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus",
                          "NameHasOwner", GLib.Variant("(s)", (WATCHER,)), GLib.VariantType("(b)"),
                          Gio.DBusCallFlags.NONE, 1000, None)
        return bool(r.unpack()[0])
    except GLib.Error:
        return False


class TrayIcon:
    """Publishes the icon and its menu until close() is called."""

    def __init__(self, on_open: Callable[[], None], on_toggle: Callable[[bool], None],
                 on_quit: Callable[[], None], title: str = "Mouse Without Borders"):
        self._on_open, self._on_toggle, self._on_quit = on_open, on_toggle, on_quit
        self._title = title
        self.menu = MenuModel()
        self._revision = 1
        self._bus = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        self._name = f"org.kde.StatusNotifierItem-{os.getpid()}-1"
        self._ids: list[int] = []
        item_info = Gio.DBusNodeInfo.new_for_xml(_ITEM_XML).interfaces[0]
        menu_info = Gio.DBusNodeInfo.new_for_xml(_MENU_XML).interfaces[0]
        self._ids.append(self._bus.register_object(ITEM_PATH, item_info, self._item_call, self._item_prop, None))
        self._ids.append(self._bus.register_object(MENU_PATH, menu_info, self._menu_call, self._menu_prop, None))
        self._owner = Gio.bus_own_name_on_connection(self._bus, self._name, Gio.BusNameOwnerFlags.NONE, None, None)
        # Register now, and again whenever the tray host restarts (for example
        # when GNOME Shell reloads its extensions).
        self._watch = Gio.bus_watch_name_on_connection(self._bus, WATCHER, Gio.BusNameWatcherFlags.NONE,
                                                       self._register, None)

    def close(self):
        Gio.bus_unwatch_name(self._watch)
        Gio.bus_unown_name(self._owner)
        for i in self._ids:
            self._bus.unregister_object(i)
        self._ids = []

    def _register(self, *_):
        self._bus.call(WATCHER, WATCHER_PATH, WATCHER, "RegisterStatusNotifierItem",
                       GLib.Variant("(s)", (self._name,)), None, Gio.DBusCallFlags.NONE, -1, None, None)

    def update(self, status: str, running: bool):
        """Show a new connection state in the menu and the tooltip."""
        if (status, running) == (self.menu.status, self.menu.running):
            return
        self.menu.status, self.menu.running = status, running
        self._revision += 1
        self._emit(MENU_PATH, "com.canonical.dbusmenu", "LayoutUpdated",
                   GLib.Variant("(ui)", (self._revision, 0)))
        self._emit(ITEM_PATH, "org.kde.StatusNotifierItem", "NewToolTip", None)

    def _emit(self, path, iface, signal, args):
        try:
            self._bus.emit_signal(None, path, iface, signal, args)
        except GLib.Error:
            pass

    # --- StatusNotifierItem ---------------------------------------------

    def _item_prop(self, _bus, _sender, _path, _iface, name):
        values = {
            "Category": GLib.Variant("s", "ApplicationStatus"),
            "Id": GLib.Variant("s", "mwb-linux"),
            "Title": GLib.Variant("s", self._title),
            "Status": GLib.Variant("s", "Active"),
            "IconName": GLib.Variant("s", ICON),
            "IconThemePath": GLib.Variant("s", ""),
            "ToolTip": GLib.Variant("(sa(iiay)ss)", (ICON, [], self._title, self.menu.status)),
            "ItemIsMenu": GLib.Variant("b", False),
            "Menu": GLib.Variant("o", MENU_PATH),
        }
        return values.get(name)

    def _item_call(self, _bus, _sender, _path, _iface, method, _params, invocation):
        if method in ("Activate", "SecondaryActivate"):
            GLib.idle_add(self._on_open)
        invocation.return_value(None)

    # --- com.canonical.dbusmenu ------------------------------------------

    def _menu_prop(self, _bus, _sender, _path, _iface, name):
        return {
            "Version": GLib.Variant("u", 3),
            "TextDirection": GLib.Variant("s", "ltr"),
            "Status": GLib.Variant("s", "normal"),
            "IconThemePath": GLib.Variant("as", []),
        }.get(name)

    def _layout(self):
        children = [GLib.Variant("(ia{sv}av)", (i, _props(p), [])) for i, p in self.menu.items()]
        return (0, {"children-display": GLib.Variant("s", "submenu")}, children)

    def _menu_call(self, _bus, _sender, _path, _iface, method, params, invocation):
        if method == "GetLayout":
            invocation.return_value(GLib.Variant("(u(ia{sv}av))", (self._revision, self._layout())))
        elif method == "GetGroupProperties":
            ids = params.unpack()[0]
            items = dict(self.menu.items())
            out = [(i, _props(items[i])) for i in ids if i in items]
            invocation.return_value(GLib.Variant("(a(ia{sv}))", (out,)))
        elif method == "GetProperty":
            i, name = params.unpack()
            value = dict(self.menu.items()).get(i, {}).get(name, "")
            invocation.return_value(GLib.Variant("(v)", (_props({name: value})[name],)))
        elif method == "Event":
            i, event = params.unpack()[:2]
            if event == "clicked":
                self._clicked(i)
            invocation.return_value(None)
        elif method == "EventGroup":
            for i, event, _data, _t in params.unpack()[0]:
                if event == "clicked":
                    self._clicked(i)
            invocation.return_value(GLib.Variant("(ai)", ([],)))
        elif method == "AboutToShow":
            invocation.return_value(GLib.Variant("(b)", (False,)))
        elif method == "AboutToShowGroup":
            invocation.return_value(GLib.Variant("(aiai)", ([], [])))
        else:
            invocation.return_dbus_error("org.freedesktop.DBus.Error.UnknownMethod", method)

    def _clicked(self, item: int):
        if item == ID_OPEN:
            GLib.idle_add(self._on_open)
        elif item == ID_TOGGLE:
            GLib.idle_add(self._on_toggle, not self.menu.running)
        elif item == ID_QUIT:
            GLib.idle_add(self._on_quit)
