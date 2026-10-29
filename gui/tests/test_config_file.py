"""Tests for mwb_gui.config_file. Run: python3 -m unittest discover gui/tests"""

import os
import stat
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from mwb_gui import config_file as cf  # noqa: E402

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))

# The installer's template, verbatim (scripts/install.sh).
INSTALLER_TEMPLATE = """\
# MWB Linux Configuration
# Get the security key from PowerToys -> Mouse Without Borders on Windows.

host = "192.168.1.100"        # Windows machine IP address
key = "YourSecurityKey"       # Security key from PowerToys MWB
name = "linux"                # This machine's name (max 15 chars)
# port = 15100                # Base port (default 15100)
# accel_multiplier = 2.0      # Linux -> Windows cursor speed
# inbound_multiplier = 1.0    # Windows -> Linux cursor speed
"""


class ParseTests(unittest.TestCase):
    def test_reads_installer_template(self):
        v = cf.parse(INSTALLER_TEMPLATE).values()
        self.assertEqual(v["host"], "192.168.1.100")
        self.assertEqual(v["key"], "YourSecurityKey")
        self.assertEqual(v["name"], "linux")
        # Absent keys report the client's defaults.
        self.assertEqual(v["peer"], "windows")
        self.assertEqual(v["edge"], "left")
        self.assertIs(v["bidirectional"], False)
        self.assertIs(v["clipboard"], True)
        self.assertEqual(v["remote_width"], 1920)

    def test_reads_debian_example(self):
        with open(os.path.join(REPO, "debian", "examples", "config.toml"), encoding="utf-8") as f:
            v = cf.parse(f.read()).values()
        self.assertEqual(v["host"], "192.168.1.100")

    def test_string_forms(self):
        doc = cf.parse('host = \'10.0.0.1\'\nkey = "a\\"b\\\\c\\u00e9"\n')
        self.assertEqual(doc.values()["host"], "10.0.0.1")
        self.assertEqual(doc.values()["key"], 'a"b\\cé')

    def test_ignores_tables_and_steps_over_multiline_values(self):
        text = (
            'host = "a"\n'
            'notes = """\n'
            'key = "not-a-key"\n'
            '"""\n'
            "list = [\n"
            '  "name = nope",\n'
            "]\n"
            "[other]\n"
            'key = "in-a-table"\n'
        )
        v = cf.parse(text).values()
        self.assertEqual(v["host"], "a")
        self.assertEqual(v["key"], "")  # neither the string body nor the table

    def test_rejects_duplicates_and_wrong_types(self):
        with self.assertRaises(cf.ConfigError):
            cf.parse('host = "a"\nhost = "b"\n')
        with self.assertRaises(cf.ConfigError):
            cf.parse("clipboard = 1\n")
        with self.assertRaises(cf.ConfigError):
            cf.parse("remote_width = true\n")


class RenderTests(unittest.TestCase):
    def test_no_change_is_byte_identical(self):
        doc = cf.parse(INSTALLER_TEMPLATE)
        self.assertEqual(cf.render(doc, doc.values()), INSTALLER_TEMPLATE + "".join(
            f"{k} = {cf._format_value(cf.DEFAULTS[k])}\n"
            for k in cf.MANAGED
            if k not in doc.entries
        ))
        only_existing = {k: doc.values()[k] for k in doc.entries}
        self.assertEqual(cf.render(doc, only_existing), INSTALLER_TEMPLATE)

    def test_replaces_value_and_keeps_comment(self):
        doc = cf.parse(INSTALLER_TEMPLATE)
        out = cf.render(doc, {"host": "10.1.2.3"})
        self.assertIn('host = "10.1.2.3"        # Windows machine IP address\n', out)
        # Every other line is untouched.
        self.assertEqual(
            [line for line in out.splitlines() if not line.startswith("host")],
            [line for line in INSTALLER_TEMPLATE.splitlines() if not line.startswith("host")],
        )

    def test_appends_new_keys_before_first_table(self):
        text = 'host = "a"\nkey = "0123456789abcdef"\n\n[future]\nx = 1\n'
        out = cf.render(cf.parse(text), {"peer": "linux", "bidirectional": True})
        self.assertEqual(
            out,
            'host = "a"\nkey = "0123456789abcdef"\npeer = "linux"\nbidirectional = true\n\n[future]\nx = 1\n',
        )

    def test_edge_changed_is_managed_next_to_edge(self):
        doc = cf.parse('host = "a"\nedge = "left"\n')
        self.assertEqual(doc.values()["edge_changed"], 0)
        out = cf.render(doc, {"edge": "right", "edge_changed": 1700000000})
        self.assertEqual(out, 'host = "a"\nedge = "right"\nedge_changed = 1700000000\n')
        self.assertEqual(cf.parse(out).values()["edge_changed"], 1700000000)

    def test_layout_list_round_trips(self):
        doc = cf.parse('host = "a"\nlayout = [ "left one", \'mid\' ,"r\\"q" ]  # order\n')
        self.assertEqual(doc.values()["layout"], ["left one", "mid", 'r"q'])
        out = cf.render(doc, {"layout": ["x", "y"], "layout_changed": 1700000000})
        self.assertEqual(out, 'host = "a"\nlayout = ["x", "y"]  # order\nlayout_changed = 1700000000\n')
        self.assertEqual(cf.parse(out).values()["layout"], ["x", "y"])
        self.assertEqual(cf.parse('host = "a"\n').values()["layout"], [])
        with self.assertRaises(cf.ConfigError):
            cf.parse("layout = [1, 2]\n")
        with self.assertRaises(cf.ConfigError):
            cf.parse('layout = [\n  "a",\n]\n')

    def test_appends_to_file_without_trailing_newline(self):
        out = cf.render(cf.parse('host = "a"'), {"edge": "right"})
        self.assertEqual(out, 'host = "a"\nedge = "right"\n')

    def test_escapes_strings(self):
        out = cf.render(cf.parse(""), {"key": 'q"u\\o\nte'})
        self.assertEqual(cf.parse(out).values()["key"], 'q"u\\o\nte')


class SaveTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.path = os.path.join(self.dir, "mwb", "config.toml")

    def test_creates_owner_only_file(self):
        cf.save({"host": "10.0.0.2", "key": "0123456789abcdefXYZ"}, self.path)
        mode = stat.S_IMODE(os.stat(self.path).st_mode)
        self.assertEqual(mode, 0o600)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.dirname(self.path)).st_mode), 0o700)
        v = cf.load(self.path).values()
        self.assertEqual((v["host"], v["key"]), ("10.0.0.2", "0123456789abcdefXYZ"))

    def test_tightens_existing_world_readable_file(self):
        os.makedirs(os.path.dirname(self.path))
        with open(self.path, "w", encoding="utf-8") as f:
            f.write(INSTALLER_TEMPLATE)
        os.chmod(self.path, 0o644)
        cf.save({"peer": "linux"}, self.path)
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)

    def test_preserves_unmanaged_settings(self):
        os.makedirs(os.path.dirname(self.path))
        original = INSTALLER_TEMPLATE + "accel_multiplier = 0.5  # slower\nlegacy_crypto = true\n"
        with open(self.path, "w", encoding="utf-8") as f:
            f.write(original)
        cf.save({"host": "desk.lan", "peer": "linux"}, self.path)
        with open(self.path, encoding="utf-8") as f:
            text = f.read()
        self.assertIn("accel_multiplier = 0.5  # slower\n", text)
        self.assertIn("legacy_crypto = true\n", text)
        self.assertIn("# Get the security key from PowerToys", text)

    def test_clear_keeps_a_private_backup_and_forgets_members(self):
        d = tempfile.mkdtemp()
        cfg = os.path.join(d, "config.toml")
        members = os.path.join(d, "members.json")
        with open(cfg, "w") as f:
            f.write('key = "secret-key-123456"\n')
        with open(members, "w") as f:
            f.write("{}")
        backup = cf.clear(cfg, members)
        self.assertEqual(backup, cfg + ".bak")
        self.assertFalse(os.path.exists(cfg))
        self.assertFalse(os.path.exists(members))
        self.assertEqual(os.stat(backup).st_mode & 0o777, 0o600)
        with open(backup) as f:
            self.assertIn("secret-key", f.read())
        self.assertEqual(cf.clear(cfg, members), "")  # nothing left: no error

    def test_follows_symlink(self):
        real = os.path.join(self.dir, "real.toml")
        with open(real, "w", encoding="utf-8") as f:
            f.write('host = "a"\n')
        os.makedirs(os.path.dirname(self.path))
        os.symlink(real, self.path)
        cf.save({"host": "b"}, self.path)
        self.assertTrue(os.path.islink(self.path))
        self.assertEqual(cf.load(real).values()["host"], "b")

    def test_save_without_tomllib(self):
        # Ubuntu 22.04's Python 3.10 has no tomllib; verification falls back
        # to this module's own parser.
        saved = cf.tomllib
        cf.tomllib = None
        try:
            cf.save({"host": "10.9.8.7", "bidirectional": True}, self.path)
        finally:
            cf.tomllib = saved
        v = cf.load(self.path).values()
        self.assertEqual((v["host"], v["bidirectional"]), ("10.9.8.7", True))

    def test_unparseable_file_is_left_alone(self):
        os.makedirs(os.path.dirname(self.path))
        broken = 'host = "unterminated\n'
        with open(self.path, "w", encoding="utf-8") as f:
            f.write(broken)
        with self.assertRaises(cf.ConfigError):
            cf.save({"host": "x"}, self.path)
        with open(self.path, encoding="utf-8") as f:
            self.assertEqual(f.read(), broken)
        self.assertEqual([n for n in os.listdir(os.path.dirname(self.path)) if n.startswith(".config-")], [])


class ValidateTests(unittest.TestCase):
    def good(self, **kw):
        v = dict(cf.DEFAULTS, host="10.0.0.2", key="0123456789abcdef")
        v.update(kw)
        return v

    def test_accepts_good_values(self):
        self.assertEqual(cf.validate(self.good()), {})

    def test_reports_problems(self):
        p = cf.validate(self.good(host=" ", key="", name="a-very-long-hostname", remote_width=10))
        self.assertEqual(set(p), {"host", "key", "name", "remote_width"})

    def test_layout_limits(self):
        self.assertEqual(cf.validate(self.good(layout=["a", "b", "c"])), {})
        for bad in (["a", "a"], ["a"] * 7, ["a", ""], ["a-very-long-hostname"]):
            self.assertIn("layout", cf.validate(self.good(layout=bad)), bad)

    def test_short_key_warns_but_does_not_block(self):
        # The client accepts any non-empty key; the GUI must not lock out a
        # working setup that uses a shorter one.
        v = self.good(key="YourSecurityKey")
        self.assertEqual(cf.validate(v), {})
        self.assertIn("key", cf.warnings(v))
        self.assertEqual(cf.warnings(self.good()), {})


class LinuxPeerDefaultsTests(unittest.TestCase):
    def test_bidirectional_default_follows_peer(self):
        # Mirrors config.BidirectionalEnabled in Go.
        self.assertIs(cf.parse('host = "a"\n').values()["bidirectional"], False)
        self.assertIs(cf.parse('host = "a"\npeer = "linux"\n').values()["bidirectional"], True)
        self.assertIs(cf.parse('peer = "linux"\nbidirectional = false\n').values()["bidirectional"], False)

    def test_host_optional_only_for_linux(self):
        base = dict(cf.DEFAULTS, key="0123456789abcdef", host="")
        self.assertIn("host", cf.validate(base))
        self.assertNotIn("host", cf.validate(dict(base, peer="linux")))


if __name__ == "__main__":
    unittest.main()
