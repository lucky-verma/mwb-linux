package group

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
)

// maxForgotten caps the tombstone set so it cannot grow without bound as
// machines are added and forgotten over a long time. Far above any real
// group, since a tombstone outlives the machine it names.
const maxForgotten = 64

// StorePath is where learned member addresses are kept between runs:
// $XDG_STATE_HOME/mwb/members.json, or ~/.local/state/mwb/members.json.
func StorePath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "mwb", "members.json")
}

// forgottenPath and requestPath sit beside the members file. forgotten.json
// records the machines this one has been told to forget, so a peer that still
// remembers one cannot gossip it back. forget-request.json is written by the
// front end (which has no live channel to the running client) to ask for a
// machine to be forgotten; the client consumes it on its next start, exactly
// as a layout change written to config.toml takes effect on restart.
func forgottenPath(storePath string) string {
	if storePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(storePath), "forgotten.json")
}

func requestPath(storePath string) string {
	if storePath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(storePath), "forget-request.json")
}

// loadMembers reads the learned addresses; a missing or unreadable file is
// an empty group, since the members are learned again on connect.
func loadMembers(path string) map[string]string {
	out := map[string]string{}
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return out
	}
	for n, addr := range m {
		if validName(n) && addr != "" && len(out) < MaxMachines {
			out[n] = addr
		}
	}
	return out
}

// saveMembers writes the learned addresses atomically, owner-only.
func saveMembers(path string, members map[string]string) error {
	return writeJSON(path, members)
}

// loadForgotten reads the tombstones: machine name to the Unix second it was
// forgotten. A missing or unreadable file is an empty set.
func loadForgotten(path string) map[string]int64 {
	out := map[string]int64{}
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var m map[string]int64
	if json.Unmarshal(data, &m) != nil {
		return out
	}
	for n, at := range m {
		if validName(n) && at > 0 && len(out) < maxForgotten {
			out[n] = at
		}
	}
	return out
}

// saveForgotten writes the tombstones atomically, owner-only.
func saveForgotten(path string, forgotten map[string]int64) error {
	return writeJSON(path, forgotten)
}

// takeForgetRequest reads the names the front end asked to forget and removes
// the request file, so a name is forgotten once and not on every start. A
// missing file is no request.
func takeForgetRequest(path string) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	_ = os.Remove(path)
	var names []string
	if json.Unmarshal(data, &names) != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		if validName(n) && !slices.Contains(out, n) && len(out) < maxForgotten {
			out = append(out, n)
		}
	}
	return out
}

// writeJSON writes v as indented JSON to path atomically, owner-only.
func writeJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
