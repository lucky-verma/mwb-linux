package group

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMembersStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "members.json")
	if got := loadMembers(path); len(got) != 0 {
		t.Fatalf("missing file gave %v", got)
	}
	want := map[string]string{"desk": "10.0.0.7", "laptop": "fe80::1%eth0"}
	if err := saveMembers(path, want); err != nil {
		t.Fatal(err)
	}
	got := loadMembers(path)
	if len(got) != 2 || got["desk"] != "10.0.0.7" || got["laptop"] != "fe80::1%eth0" {
		t.Fatalf("got %v", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode().Perm(), err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadMembers(path); len(got) != 0 {
		t.Fatalf("corrupt file gave %v", got)
	}
}

func TestForgottenStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "members.json")
	fpath := forgottenPath(path)
	if got := loadForgotten(fpath); len(got) != 0 {
		t.Fatalf("missing file gave %v", got)
	}
	want := map[string]int64{"gone": 1_700_000_000, "old": 1}
	if err := saveForgotten(fpath, want); err != nil {
		t.Fatal(err)
	}
	got := loadForgotten(fpath)
	if len(got) != 2 || got["gone"] != 1_700_000_000 || got["old"] != 1 {
		t.Fatalf("got %v", got)
	}
	if info, err := os.Stat(fpath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode().Perm(), err)
	}
}

func TestTakeForgetRequestConsumesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "members.json")
	rpath := requestPath(path)
	if got := takeForgetRequest(rpath); got != nil {
		t.Fatalf("missing file gave %v", got)
	}
	if err := os.MkdirAll(filepath.Dir(rpath), 0o700); err != nil {
		t.Fatal(err)
	}
	// A duplicate and an invalid name are dropped; the rest come through once.
	if err := os.WriteFile(rpath, []byte(`["gone","gone","","this-name-is-far-too-long"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := takeForgetRequest(rpath); len(got) != 1 || got[0] != "gone" {
		t.Fatalf("got %v", got)
	}
	// The request is removed, so a machine is forgotten once, not every start.
	if _, err := os.Stat(rpath); !os.IsNotExist(err) {
		t.Fatalf("request file not consumed: err %v", err)
	}
}
