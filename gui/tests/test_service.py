"""Tests for mwb_gui.service that need no systemd or GTK."""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from mwb_gui import service  # noqa: E402


class ExecStartTests(unittest.TestCase):
    def test_parses_systemd_exec_start(self):
        value = ("{ path=/usr/bin/mwb ; argv[]=/usr/bin/mwb -bidi -edge left ; ignore_errors=no ; "
                 "start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }")
        self.assertEqual(service._parse_exec_start(value), ["/usr/bin/mwb", "-bidi", "-edge", "left"])
        self.assertIsNone(service._parse_exec_start(""))

    def test_overriding_flags(self):
        # debian/mwb.service passes these, which beat config.toml.
        u = service.UnitState(exec_args=["/usr/bin/mwb", "-bidi", "-edge", "left"])
        self.assertEqual(u.overriding_flags(), ["-bidi", "-edge"])
        u = service.UnitState(exec_args=["/usr/local/bin/mwb", "-debug"])
        self.assertEqual(u.overriding_flags(), [])
        self.assertEqual(service.UnitState().overriding_flags(), [])

    def test_control_rejects_unknown_actions(self):
        with self.assertRaises(ValueError):
            service.control("rm -rf")


class StatusFileTests(unittest.TestCase):
    def write(self, data):
        fd, path = tempfile.mkstemp()
        with os.fdopen(fd, "w") as f:
            json.dump(data, f)
        self.addCleanup(os.unlink, path)
        return path

    def test_live_status(self):
        path = self.write({"state": "connected", "remote": "desk", "pid": os.getpid()})
        self.assertEqual(service.read_status(path)["remote"], "desk")

    def test_stale_status_from_dead_process_is_ignored(self):
        pid = os.fork()
        if pid == 0:
            os._exit(0)
        os.waitpid(pid, 0)
        path = self.write({"state": "connected", "pid": pid})
        self.assertIsNone(service.read_status(path))

    def test_recent_key_mismatch(self):
        from datetime import datetime, timedelta, timezone
        now = datetime.now(timezone.utc)
        fresh = now.strftime("%Y-%m-%dT%H:%M:%SZ")
        stale = (now - timedelta(seconds=120)).strftime("%Y-%m-%dT%H:%M:%SZ")
        self.assertEqual(
            service.recent_key_mismatch({"key_mismatch_from": "10.0.0.5:4", "key_mismatch_at": fresh}),
            "10.0.0.5:4")
        self.assertIsNone(
            service.recent_key_mismatch({"key_mismatch_from": "10.0.0.5:4", "key_mismatch_at": stale}))
        self.assertIsNone(service.recent_key_mismatch({"key_mismatch_at": fresh}))
        self.assertIsNone(service.recent_key_mismatch({"key_mismatch_from": "x", "key_mismatch_at": "bad"}))
        self.assertIsNone(service.recent_key_mismatch(None))

    def test_missing_or_corrupt(self):
        self.assertIsNone(service.read_status("/nonexistent/status.json"))
        fd, path = tempfile.mkstemp()
        os.write(fd, b"{not json")
        os.close(fd)
        self.addCleanup(os.unlink, path)
        self.assertIsNone(service.read_status(path))
        self.assertIsNone(service.read_status(""))

    def test_request_forget_writes_and_merges(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        self.assertEqual(service.request_forget("mystogan", d), "")
        path = os.path.join(d, "forget-request.json")
        with open(path) as f:
            self.assertEqual(json.load(f), ["mystogan"])
        # A second name is appended; a repeat is not duplicated.
        service.request_forget("tv", d)
        service.request_forget("mystogan", d)
        with open(path) as f:
            self.assertEqual(sorted(json.load(f)), ["mystogan", "tv"])

    def test_client_version_parses_and_caches(self):
        import subprocess as sp
        calls = []

        def fake_run(args, **kw):
            calls.append(args)
            return sp.CompletedProcess(args, 0, stdout="mwb 0.6.4-linuxpeer.24\n", stderr="")

        service._client_version = None
        orig = service.subprocess.run
        service.subprocess.run = fake_run
        try:
            self.assertEqual(service.client_version(), "0.6.4-linuxpeer.24")
            self.assertEqual(service.client_version(), "0.6.4-linuxpeer.24")  # cached
            self.assertEqual(len(calls), 1, "version should be read once and cached")
        finally:
            service.subprocess.run = orig
            service._client_version = None

    def test_forgotten_names_reads_both_files(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        self.assertEqual(service.forgotten_names(d), set())
        with open(os.path.join(d, "forgotten.json"), "w") as f:
            json.dump({"mystogan": 123, "tv": 456}, f)
        with open(os.path.join(d, "forget-request.json"), "w") as f:
            json.dump(["pending", "mystogan"], f)
        self.assertEqual(service.forgotten_names(d), {"mystogan", "tv", "pending"})
        # Corrupt files are ignored, not fatal.
        with open(os.path.join(d, "forgotten.json"), "w") as f:
            f.write("{not json")
        self.assertEqual(service.forgotten_names(d), {"pending", "mystogan"})

    def test_grid_extent_and_rects(self):
        self.assertEqual(service.grid_extent({}), (0, 0))
        cells = {"a": (0, 0), "b": (1, 0), "c": (0, 1)}
        self.assertEqual(service.grid_extent(cells), (2, 2))
        rects = service.grid_rects(cells, {}, 800, 400, pad=10, label_h=40)
        self.assertEqual(set(rects), {"a", "b", "c"})
        for name, (x, y, w, h) in rects.items():
            self.assertGreater(w, 0)
            self.assertGreater(h, 0)
            self.assertGreaterEqual(x, 0)
            self.assertGreaterEqual(y, 0)
            self.assertLessEqual(x + w, 800 + 1)
            self.assertLessEqual(y + h, 400 + 1)
        # Column 0 cells ("a","c") share a horizontal centre; "b" sits to the right.
        self.assertLess(rects["a"][0], rects["b"][0])
        self.assertAlmostEqual(rects["a"][0], rects["c"][0], delta=1.0)
        self.assertLess(rects["a"][1], rects["c"][1])  # a above c

    def test_place_adjacent_and_renormalize(self):
        # A row a-b-c; drop c below a -> c starts a new row under a.
        row = {"a": (0, 0), "b": (1, 0), "c": (2, 0)}
        got = service.place_adjacent(row, "c", "a", "down")
        self.assertEqual(got, {"a": (0, 0), "b": (1, 0), "c": (0, 1)})
        # Swap a and c.
        self.assertEqual(service.place_adjacent(row, "a", "c", "center"),
                         {"a": (2, 0), "b": (1, 0), "c": (0, 0)})
        # Insert c to the left of a -> new leftmost column.
        got = service.place_adjacent(row, "c", "a", "left")
        self.assertEqual(got["c"], (0, 0))
        self.assertEqual(got["a"], (1, 0))
        self.assertEqual(got["b"], (2, 0))
        # A column; drop bottom beside top -> becomes a 2-wide row (shape change).
        col = {"a": (0, 0), "b": (0, 1)}
        self.assertEqual(service.place_adjacent(col, "b", "a", "right"),
                         {"a": (0, 0), "b": (1, 0)})
        # No-op / bad side returns integer cells unchanged.
        self.assertEqual(service.place_adjacent(row, "a", "a", "left"), row)

    def test_move_in_grid(self):
        # A row a-b-c; move b down -> b drops to the row below, under its column.
        row = {"a": (0, 0), "b": (1, 0), "c": (2, 0)}
        got = service.move_in_grid(row, "b", "down")
        self.assertEqual(got, {"a": (0, 0), "b": (1, 1), "c": (2, 0)})
        # Move a right -> swaps with b.
        self.assertEqual(service.move_in_grid(row, "a", "right"),
                         {"a": (1, 0), "b": (0, 0), "c": (2, 0)})
        # Move leftmost further left -> opens a new leftmost column (compacted).
        self.assertEqual(service.move_in_grid(row, "a", "left"), row)  # a already at 0 -> no net change
        # In a column, move top up -> new top row (no-op after compaction here).
        col = {"a": (0, 0), "b": (0, 1)}
        self.assertEqual(service.move_in_grid(col, "b", "up"),
                         {"a": (0, 1), "b": (0, 0)})  # swap
        self.assertEqual(service.move_in_grid(row, "a", "nowhere"), row)

    def test_drop_target(self):
        rects = {"a": (0, 0, 100, 100), "b": (200, 0, 100, 100)}
        self.assertEqual(service.drop_target(rects, 50, 50), ("a", "center"))
        self.assertEqual(service.drop_target(rects, 95, 50)[1], "right")   # right edge of a
        self.assertEqual(service.drop_target(rects, 250, 5)[0], "b")       # over b
        self.assertEqual(service.drop_target(rects, 250, 95)[1], "down")   # bottom of b
        self.assertEqual(service.drop_target({}, 0, 0), (None, ""))

    def test_forgettable_gating(self):
        f = service.forgettable
        # Running: only an offline, named peer that is not this computer.
        self.assertTrue(f("tv", "desk", {"tv"}, running=True, linux_group=True))
        self.assertFalse(f("tv", "desk", set(), running=True, linux_group=True))  # online
        self.assertFalse(f("desk", "desk", {"desk"}, running=True, linux_group=True))  # self
        self.assertFalse(f("tv", "desk", {"tv"}, running=True, linux_group=False))  # windows pairing
        self.assertFalse(f("", "desk", set(), running=True, linux_group=True))
        # Stopped: state unknown, so any other named machine may be forgotten.
        self.assertTrue(f("tv", "desk", set(), running=False, linux_group=True))
        self.assertFalse(f("desk", "desk", set(), running=False, linux_group=True))

    def test_request_forget_rejects_impossible_names(self):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        for bad in ("", "   ", "this-name-is-far-too-long"):
            with self.assertRaises(ValueError):
                service.request_forget(bad, d)

    def test_status_path_needs_runtime_dir(self):
        old = os.environ.get("XDG_RUNTIME_DIR")
        try:
            os.environ["XDG_RUNTIME_DIR"] = ""
            self.assertEqual(service.status_path(), "")
            os.environ["XDG_RUNTIME_DIR"] = "/run/user/1000"
            self.assertEqual(service.status_path(), "/run/user/1000/mwb/status.json")
        finally:
            if old is None:
                os.environ.pop("XDG_RUNTIME_DIR", None)
            else:
                os.environ["XDG_RUNTIME_DIR"] = old



class UinputDiagnosisTests(unittest.TestCase):
    def test_each_cause(self):
        d = service.diagnose_uinput
        self.assertIsNone(d(True, True, True, True, True))
        self.assertIn("modprobe", d(False, False, False, False, False)[1])
        # Right after installing: in the group on disk, not in this session.
        self.assertEqual(d(True, False, True, True, False)[0], "Restart the computer to finish setup")
        self.assertIn("usermod", d(True, False, True, False, False)[1])
        self.assertIn("groupadd", d(True, False, False, False, False)[1])
        self.assertIn("ls -l /dev/uinput", d(True, False, True, True, True)[1])

    def test_uinput_problem_missing_device(self):
        self.assertIn("isn't loaded", service.uinput_problem("/nonexistent/uinput")[0])


class CrashLoopTests(unittest.TestCase):
    def test_auto_restart_is_not_running(self):
        # What systemd reports between restarts of a failing service, the
        # state the window wrongly showed as "Running".
        u = service.UnitState(installed=True, active="activating", sub="auto-restart")
        self.assertTrue(u.crash_looping)
        self.assertFalse(u.running)

    def test_healthy_states(self):
        self.assertTrue(service.UnitState(active="active", sub="running").running)
        self.assertTrue(service.UnitState(active="activating", sub="start").running)
        self.assertFalse(service.UnitState(active="active", sub="running").crash_looping)


class LanAddressTests(unittest.TestCase):
    def test_matches_go_is_private(self):
        for ip, want in {"10.0.0.5": True, "172.16.4.2": True, "172.31.255.1": True,
                         "192.168.1.5": True, "172.32.0.1": False, "192.0.2.2": False,
                         "127.0.0.1": False, "8.8.8.8": False, "nonsense": False}.items():
            self.assertIs(service.is_lan_address(ip), want, ip)

    def test_local_address_is_lan_or_empty(self):
        ip = service.local_address()
        self.assertTrue(ip == "" or service.is_lan_address(ip))


class DescribeConnectionTests(unittest.TestCase):
    base = {"state": "connected", "remote": "beta-pc", "edge": "left"}

    def d(self, **kw):
        return service.describe_connection(dict(self.base, **kw))

    def test_windows_peer_unchanged(self):
        self.assertEqual(self.d(bidirectional=False)[2], "The other computer can control this one.")
        self.assertIn("highlighted edge", self.d(bidirectional=True)[2])

    def test_both_share(self):
        kind, _, detail = self.d(bidirectional=True, sharing=True, peer_sharing=True, peer_edge="right")
        self.assertEqual(kind, "ok")
        self.assertIn("either computer", detail)

    def test_neither_shares(self):
        # The situation that looked like "connected but nothing happens".
        kind, title, detail = self.d(bidirectional=False, sharing=False, peer_sharing=False, peer_edge="right")
        self.assertEqual(kind, "wait")
        self.assertIn("neither computer shares", title)
        self.assertIn(service.CONTROL_SWITCH, detail)

    def test_only_peer_shares(self):
        kind, _, detail = self.d(bidirectional=False, sharing=False, peer_sharing=True, peer_edge="right")
        self.assertEqual(kind, "ok")
        self.assertIn("beta-pc can control this computer", detail)

    def test_only_here_shares(self):
        _, _, detail = self.d(bidirectional=True, sharing=True, peer_sharing=False, peer_edge="right")
        self.assertIn("This computer controls beta-pc", detail)

    def test_capture_failed(self):
        _, _, detail = self.d(bidirectional=True, sharing=False, peer_sharing=True, peer_edge="right")
        self.assertIn("Xorg", detail)

    def test_side_clash(self):
        kind, title, _ = self.d(bidirectional=True, sharing=True, peer_sharing=True, peer_edge="left")
        self.assertEqual(kind, "error")
        self.assertIn("both put the other on the left", title)

    def test_side_clash_says_which_way_to_fix(self):
        _, _, detail = self.d(bidirectional=True, sharing=True, peer_sharing=True,
                              edge="left", peer_edge="left", remote="beta-pc")
        self.assertIn("sits to the right of this computer", detail)
        self.assertIn("swap them on beta-pc", detail)


class GroupDescriptionTests(unittest.TestCase):
    def test_join_names(self):
        self.assertEqual(service.join_names([]), "")
        self.assertEqual(service.join_names(["a"]), "a")
        self.assertEqual(service.join_names(["a", "b"]), "a and b")
        self.assertEqual(service.join_names(["a", "b", "c"]), "a, b and c")

    def test_names_online_and_offline_machines(self):
        st = {"state": "connected", "group": True, "sharing": True, "members": [
            {"name": "desk", "online": True, "current": True},
            {"name": "tv", "online": False, "current": False},
            {"name": "mini", "online": True, "current": True},
        ]}
        kind, title, detail = service.describe_group(st)
        self.assertEqual((kind, title), ("ok", "Connected to desk and mini"))
        self.assertIn("tv is offline", detail)
        self.assertIn("highlighted edge", detail)

    def test_layout_set_on_another_machine_is_called_out(self):
        st = {"state": "connected", "group": True, "sharing": True, "layout_by": "desk",
              "members": [{"name": "desk", "online": True, "current": True}]}
        _, _, detail = service.describe_group(st)
        self.assertIn("screen order was set on desk", detail)
        self.assertIn("drag the screens", detail)

    def test_layout_set_here_is_not_called_out(self):
        # This machine's own name is never in members, so a layout_by that is
        # not a member means we set the order; no remote-order note.
        st = {"state": "connected", "group": True, "sharing": True, "layout_by": "me",
              "members": [{"name": "desk", "online": True, "current": True}]}
        _, _, detail = service.describe_group(st)
        self.assertNotIn("screen order was set on", detail)

    def test_older_member_is_an_error(self):
        st = {"group": True, "members": [{"name": "old", "online": True, "current": False}]}
        kind, _, detail = service.describe_group(st)
        self.assertEqual(kind, "error")
        self.assertIn("old runs an older mwb", detail)
        self.assertIn("stay here", detail)


class InterfaceTests(unittest.TestCase):
    def test_parses_and_classifies_interfaces(self):
        sysfs = tempfile.mkdtemp()
        os.makedirs(os.path.join(sysfs, "wlp4s0", "wireless"))
        os.makedirs(os.path.join(sysfs, "enp0s31f6", "device"))
        text = json.dumps([
            {"ifname": "lo", "flags": ["LOOPBACK", "UP"], "operstate": "UNKNOWN",
             "addr_info": [{"family": "inet", "local": "127.0.0.1"}]},
            {"ifname": "enp0s31f6", "flags": ["NO-CARRIER"], "operstate": "DOWN", "addr_info": []},
            {"ifname": "wlp4s0", "operstate": "UP", "addr_info": [
                {"family": "inet6", "local": "fe80::1"}, {"family": "inet", "local": "192.168.1.20"}]},
            {"ifname": "docker0", "operstate": "DOWN", "addr_info": [{"family": "inet", "local": "172.17.0.1"}]},
            {"ifname": "tailscale0", "operstate": "UNKNOWN", "addr_info": [{"family": "inet", "local": "100.64.1.2"}]},
        ])
        got = {i.name: i for i in service.parse_ip_json(text, sysfs)}
        self.assertNotIn("lo", got)
        self.assertEqual((got["wlp4s0"].kind, got["wlp4s0"].address, got["wlp4s0"].up), ("Wi-Fi", "192.168.1.20", True))
        self.assertEqual((got["enp0s31f6"].kind, got["enp0s31f6"].up), ("Ethernet", False))
        self.assertIn("not connected", got["enp0s31f6"].label())
        self.assertTrue(got["docker0"].hidden)
        self.assertEqual(got["tailscale0"].kind, "VPN")
        self.assertEqual(list(got)[0], "wlp4s0")  # connected ones first
        self.assertEqual(service.parse_ip_json("not json"), [])

    def test_pick_address_matches_the_client(self):
        self.assertEqual(service.pick_address(["fe80::1", "2001:db8::5", "192.168.1.7"]), "192.168.1.7")
        self.assertEqual(service.pick_address(["100.64.0.9", "10.0.0.4"]), "10.0.0.4")
        self.assertEqual(service.pick_address(["fe80::1", "100.64.0.9"]), "100.64.0.9")
        self.assertEqual(service.pick_address(["fe80::1", "169.254.3.3"]), "")


if __name__ == "__main__":
    unittest.main()
