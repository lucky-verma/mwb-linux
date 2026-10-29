// internal/config/edge_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSetEdgeLines(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		edge           string
		changed        int64
	}{
		{
			name:    "replace both, keep comments and other keys",
			in:      "# mine\nhost = \"10.0.0.2\"\nedge = \"right\"  # was here\nedge_changed = 5\nkey = \"k\"\n",
			edge:    "left",
			changed: 1700000000,
			want:    "# mine\nhost = \"10.0.0.2\"\nedge = \"left\"\nedge_changed = 1700000000\nkey = \"k\"\n",
		},
		{
			name:    "add missing keys at the end",
			in:      "host = \"h\"\nedge = \"right\"",
			edge:    "left",
			changed: 42,
			want:    "host = \"h\"\nedge = \"left\"\nedge_changed = 42\n",
		},
		{
			name:    "add above the first table",
			in:      "host = \"h\"\n\n[extra]\nedge = \"keep\"\n",
			edge:    "right",
			changed: 7,
			want:    "host = \"h\"\nedge = \"right\"\nedge_changed = 7\n\n[extra]\nedge = \"keep\"\n",
		},
		{
			name: "unknown time removes edge_changed",
			in:   "edge = \"left\"\nedge_changed = 9\nhost = \"h\"\n",
			edge: "right",
			want: "edge = \"right\"\nhost = \"h\"\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := setEdgeLines(tc.in, tc.edge, tc.changed); got != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestSaveEdgeKeepsFileUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	orig := "# settings\nhost = \"10.0.0.5\"\nkey = \"abcdefghijklmnop\"\npeer = \"linux\"\nedge = \"right\"\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveEdge(path, "left", 1700000000); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Edge != "left" || cfg.EdgeChanged != 1700000000 || cfg.Host != "10.0.0.5" {
		t.Fatalf("after SaveEdge: %+v", cfg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	if err := SaveEdge(path, "up", 0); err == nil {
		t.Fatal("an invalid edge was saved")
	}
}

func TestEdgeAgeMinutes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const unknown = 1<<24 - 1
	for _, tc := range []struct {
		changed int64
		want    uint32
	}{
		{0, unknown},
		{now.Unix() + 60, unknown}, // clock skew: a future time is not trusted
		{now.Unix() - 59, 0},
		{now.Unix() - 600, 10},
		{1, unknown}, // decades ago: capped
	} {
		c := &Config{EdgeChanged: tc.changed}
		if got := c.EdgeAgeMinutes(now, unknown); got != tc.want {
			t.Errorf("changed=%d: age %d, want %d", tc.changed, got, tc.want)
		}
	}
}
