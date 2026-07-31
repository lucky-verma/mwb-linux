//go:build linux

package filetransfer

import (
	"fmt"
	"syscall"
)

// freeSpaceReserve keeps a margin free so a transfer cannot fill the filesystem
// to the point where the desktop session itself starts failing.
const freeSpaceReserve = 64 * 1024 * 1024

// checkFreeSpace refuses a transfer that would not fit, before any bytes are
// read. Without this the size cap alone still lets a peer fill a small
// partition, since the cap bounds one transfer rather than the disk.
//
// A filesystem that cannot be queried is allowed through: failing closed here
// would break transfers on filesystems that do not implement statfs, and the
// size cap plus the write itself still bound the damage.
func checkFreeSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil
	}

	//nolint:gosec // Bavail and Bsize are kernel-supplied and non-negative.
	avail := int64(st.Bavail) * int64(st.Bsize)
	if avail-freeSpaceReserve < need {
		return fmt.Errorf("%w: %d bytes needed, %d available in %s",
			ErrSizeRejected, need, avail, dir)
	}
	return nil
}
