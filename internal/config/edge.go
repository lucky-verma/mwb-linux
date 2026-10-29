// internal/config/edge.go
package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// EdgeAgeMinutes is how long ago edge was chosen, in whole minutes, or
// unknown when edge_changed is unset or in the future.
func (c *Config) EdgeAgeMinutes(now time.Time, unknown uint32) uint32 {
	return EdgeAge(c.EdgeChanged, now, unknown)
}

// EdgeAge is the age in whole minutes of a side chosen at changed (Unix
// seconds), or unknown when changed is unset or in the future.
func EdgeAge(changed int64, now time.Time, unknown uint32) uint32 {
	if changed <= 0 {
		return unknown
	}
	secs := now.Unix() - changed
	if secs < 0 {
		return unknown
	}
	return uint32(min(secs/60, int64(unknown)))
}

var (
	keyLine   = regexp.MustCompile(`^[ \t]*([A-Za-z0-9_-]+)[ \t]*=`)
	tableLine = regexp.MustCompile(`^[ \t]*\[`)
)

// SaveEdge rewrites edge and edge_changed in the config file at path, keeping
// every other line, comment and permission as it is. changed <= 0 removes
// edge_changed. The file is replaced atomically, so a reader never sees half
// of it.
func SaveEdge(path, edge string, changed int64) error {
	if edge != "left" && edge != "right" {
		return fmt.Errorf("edge must be left or right, got %q", edge)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return err
	}
	text := setEdgeLines(string(data), edge, changed)

	var check Config
	if _, err := toml.Decode(text, &check); err != nil {
		return fmt.Errorf("updated config would not parse: %w", err)
	}
	if check.Edge != edge || (changed > 0 && check.EdgeChanged != changed) {
		return fmt.Errorf("edge did not survive the update")
	}

	return writeConfigAtomic(real, text)
}

// setEdgeLines replaces the top-level edge and edge_changed lines, or adds
// them above the first table so they stay top-level keys.
func setEdgeLines(text, edge string, changed int64) string {
	want := map[string]string{"edge": fmt.Sprintf("edge = %q\n", edge)}
	if changed > 0 {
		want["edge_changed"] = "edge_changed = " + strconv.FormatInt(changed, 10) + "\n"
	}
	return setKeyLines(text, []string{"edge", "edge_changed"}, want)
}

// setKeyLines replaces the top-level lines for keys with want[key], removes
// the ones want leaves out, and adds missing ones above the first table so
// they stay top-level keys. Every other line is kept as it is.
func setKeyLines(text string, keys []string, want map[string]string) string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	managed := make(map[string]bool, len(keys))
	for _, k := range keys {
		managed[k] = true
	}
	want = maps.Clone(want)

	out := make([]string, 0, len(lines)+len(keys))
	tableAt := -1
	for _, line := range lines {
		if tableAt < 0 && tableLine.MatchString(line) {
			tableAt = len(out)
		}
		if tableAt < 0 {
			if m := keyLine.FindStringSubmatch(line); m != nil && managed[m[1]] {
				if repl, ok := want[m[1]]; ok {
					out = append(out, repl)
					delete(want, m[1])
				}
				continue // a removed key, or a duplicate
			}
		}
		out = append(out, line)
	}

	var add []string
	for _, k := range keys {
		if v, ok := want[k]; ok {
			add = append(add, v)
		}
	}
	if len(add) > 0 {
		at := len(out)
		if tableAt >= 0 {
			at = tableAt
			for at > 0 && strings.TrimSpace(out[at-1]) == "" {
				at--
			}
			if strings.TrimSpace(out[at]) != "" {
				add = append(add, "\n") // keep a blank line before the table
			}
		}
		if at > 0 && !strings.HasSuffix(out[at-1], "\n") {
			out[at-1] += "\n"
		}
		out = append(out[:at], append(add, out[at:]...)...)
	}
	return strings.Join(out, "")
}

// writeConfigAtomic replaces the file at real (no symlinks) with text,
// owner-only, so a reader never sees half of it.
func writeConfigAtomic(real, text string) error {
	tmp, err := os.CreateTemp(filepath.Dir(real), ".config-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	// The file holds the security key: owner-only before any byte is written.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), real)
}
