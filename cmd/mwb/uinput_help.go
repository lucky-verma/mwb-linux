// cmd/mwb/uinput_help.go
package main

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"slices"
	"strconv"
)

// uinputState is what decides why /dev/uinput could not be opened.
type uinputState struct {
	err         error // from opening /dev/uinput
	inGroupDB   bool  // the account is listed in the input group (/etc/group)
	inGroupNow  bool  // this process actually holds the input group
	groupExists bool  // an input group exists at all
}

// genericUinputSteps is the original setup list, kept for errors that match
// none of the specific cases.
const genericUinputSteps = `Setup required:
  1. sudo modprobe uinput
  2. echo 'uinput' | sudo tee /etc/modules-load.d/uinput.conf
  3. echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-uinput.rules
  4. sudo udevadm control --reload-rules && sudo udevadm trigger /dev/uinput
  5. Ensure your user is in the 'input' group: sudo usermod -aG input $USER`

// uinputHelp explains the most likely cause and the one fix that applies,
// instead of listing every setup step.
func uinputHelp(s uinputState) string {
	switch {
	case errors.Is(s.err, fs.ErrNotExist):
		return `/dev/uinput does not exist: the uinput kernel module is not loaded.
Fix:
  sudo modprobe uinput
  echo uinput | sudo tee /etc/modules-load.d/uinput.conf   # load it at every boot`

	case errors.Is(s.err, fs.ErrPermission) && s.inGroupDB && !s.inGroupNow:
		// The common case right after installing: usermod changed /etc/group,
		// but group membership is fixed when a session starts, and the
		// systemd user manager running this service started before that.
		return `You are in the 'input' group, but this session started before you were added,
so it does not have the permission yet.
Fix: restart the computer. Logging out is often not enough, because your
systemd user session (which runs the mwb service) can stay running.`

	case errors.Is(s.err, fs.ErrPermission) && !s.inGroupDB:
		if !s.groupExists {
			return `There is no 'input' group on this system, so /dev/uinput cannot be shared with you.
Fix:
  sudo groupadd --system input
  sudo usermod -aG input $USER
  echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-mwb-uinput.rules
  sudo udevadm control --reload-rules && sudo udevadm trigger --sysname-match=uinput
Then restart the computer.`
		}
		return `Your account is not in the 'input' group, which may use /dev/uinput.
Fix:
  sudo usermod -aG input $USER
Then restart the computer so the new group applies.`

	case errors.Is(s.err, fs.ErrPermission):
		// In the group, and the session has it, yet still refused: the device
		// node does not carry the group permission, so the udev rule is
		// missing or has not been applied to it.
		return `You are in the 'input' group, but /dev/uinput is not open to that group.
Check:  ls -l /dev/uinput     (expected: crw-rw---- 1 root input)
Fix:
  echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-mwb-uinput.rules
  sudo udevadm control --reload-rules && sudo udevadm trigger --sysname-match=uinput`
	}
	return genericUinputSteps
}

// currentUinputState gathers the group facts for err. Lookups that fail are
// treated as "not in the group", which only ever leads to the more general
// advice.
func currentUinputState(err error) uinputState {
	s := uinputState{err: err}
	g, gerr := user.LookupGroup("input")
	if gerr != nil {
		return s
	}
	s.groupExists = true
	if u, uerr := user.Current(); uerr == nil {
		if ids, ierr := u.GroupIds(); ierr == nil {
			s.inGroupDB = slices.Contains(ids, g.Gid)
		}
	}
	if gid, perr := strconv.Atoi(g.Gid); perr == nil {
		if have, herr := os.Getgroups(); herr == nil {
			s.inGroupNow = slices.Contains(have, gid) || os.Getegid() == gid
		}
	}
	return s
}
