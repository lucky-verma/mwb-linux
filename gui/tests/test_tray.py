"""Tests for mwb_gui.tray that need no D-Bus session or GTK."""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

try:
    from mwb_gui import tray  # noqa: E402
except ImportError:  # PyGObject missing: nothing here can run
    tray = None


@unittest.skipIf(tray is None, "needs PyGObject")
class TrayStatusTests(unittest.TestCase):
    def test_not_running(self):
        self.assertEqual(tray.tray_status(None), ("Not running", False))

    def test_connected_names_the_peer(self):
        text, running = tray.tray_status({"state": "connected", "remote": "desk", "bidirectional": True})
        self.assertTrue(running)
        self.assertIn("desk", text)

    def test_group_names_the_members(self):
        st = {"state": "connected", "group": True, "members": [
            {"name": "desk", "online": True, "current": True},
            {"name": "mini", "online": True, "current": True}]}
        self.assertEqual(tray.tray_status(st), ("Connected to desk and mini", True))

    def test_waiting_and_connecting(self):
        self.assertEqual(tray.tray_status({"state": "connecting", "waiting": True})[0],
                         "Waiting for the other computer")
        self.assertIn("10.0.0.2", tray.tray_status({"state": "connecting", "host": "10.0.0.2"})[0])


@unittest.skipIf(tray is None, "needs PyGObject")
class MenuModelTests(unittest.TestCase):
    def test_toggle_follows_running_and_quit_is_last(self):
        m = tray.MenuModel()
        labels = lambda: [p.get("label") for _, p in m.items()]  # noqa: E731
        self.assertIn("Start sharing", labels())
        m.running = True
        self.assertIn("Stop sharing", labels())
        self.assertEqual(m.items()[-1], (tray.ID_QUIT, {"label": "Quit"}))
        ids = [i for i, _ in m.items()]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertNotIn(0, ids)  # 0 is the menu root


if __name__ == "__main__":
    unittest.main()
