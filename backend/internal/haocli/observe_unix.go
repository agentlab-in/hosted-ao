//go:build !windows

package haocli

import (
	"os"
	"syscall"
)

func statFile(path string) (FileObservation, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileObservation{}, err
	}
	uid := -1
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid = int(stat.Uid)
	}
	return FileObservation{Mode: info.Mode(), UID: uid, Owner: uid < 0 || uid == os.Geteuid()}, nil
}

func diskAvailable(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, nil
	}
	// Bsize is non-negative after the guard above. Its signedness differs by OS.
	//nolint:gosec,unconvert // Linux uses int64 while Darwin uses uint32.
	return stat.Bavail * uint64(stat.Bsize), nil
}
