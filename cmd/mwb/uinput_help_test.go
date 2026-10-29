// cmd/mwb/uinput_help_test.go
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
)

// Errors shaped exactly as internal/input returns them.
func openErr(errno syscall.Errno) error {
	return fmt.Errorf("open /dev/uinput: %w", &os.PathError{Op: "open", Path: "/dev/uinput", Err: errno})
}

func TestUinputHelpPicksTheCause(t *testing.T) {
	cases := []struct {
		name string
		s    uinputState
		want string
	}{
		{"module not loaded", uinputState{err: openErr(syscall.ENOENT), groupExists: true}, "sudo modprobe uinput"},
		// The case right after installing the .deb: added to the group, but
		// the running session predates it.
		{"stale session", uinputState{err: openErr(syscall.EACCES), groupExists: true, inGroupDB: true}, "restart the computer"},
		{"not in group", uinputState{err: openErr(syscall.EACCES), groupExists: true}, "sudo usermod -aG input $USER"},
		{"no group", uinputState{err: openErr(syscall.EACCES)}, "sudo groupadd --system input"},
		{"device not shared", uinputState{err: openErr(syscall.EACCES), groupExists: true, inGroupDB: true, inGroupNow: true}, "ls -l /dev/uinput"},
		{"unknown error", uinputState{err: errors.New("something else")}, "Setup required:"},
	}
	for _, c := range cases {
		got := uinputHelp(c.s)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: help does not mention %q:\n%s", c.name, c.want, got)
		}
	}
}

func TestUinputHelpMatchesWrappedErrno(t *testing.T) {
	// errors.Is must see through the wrapping internal/input adds.
	if !errors.Is(openErr(syscall.EACCES), fs.ErrPermission) || !errors.Is(openErr(syscall.ENOENT), fs.ErrNotExist) {
		t.Fatal("wrapped errno no longer matches fs errors")
	}
}

func TestUinputHelpPrintsNoDuplicateLines(t *testing.T) {
	// The original output printed its last line twice.
	for _, s := range []uinputState{{err: errors.New("x")}, {err: openErr(syscall.EACCES), groupExists: true}} {
		seen := map[string]bool{}
		for _, line := range strings.Split(uinputHelp(s), "\n") {
			if line != "" && seen[line] {
				t.Fatalf("duplicate line %q", line)
			}
			seen[line] = true
		}
	}
}

// currentUinputState must not fail or panic on a system without an input
// group (this test container is one).
func TestCurrentUinputStateIsSafe(t *testing.T) {
	_ = currentUinputState(openErr(syscall.EACCES))
}
