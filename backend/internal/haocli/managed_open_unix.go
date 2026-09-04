//go:build !windows

package haocli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openManagedRegular(path string, maxSize int64) (*os.File, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	parts := strings.FieldsFunc(abs, func(r rune) bool { return r == filepath.Separator })
	if len(parts) == 0 {
		return nil, errors.New("managed path has no file component")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errors.New("managed path contains an inaccessible or linked ancestor")
		}
		fd = next
	}
	defer func() { _ = unix.Close(fd) }()
	name := parts[len(parts)-1]
	var before unix.Stat_t
	if err := unix.Fstatat(fd, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size > maxSize {
		return nil, errors.New("managed file is not a bounded regular file")
	}
	fileFD, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("managed file could not be opened without following links")
	}
	var opened, after unix.Stat_t
	if err := unix.Fstat(fileFD, &opened); err != nil {
		_ = unix.Close(fileFD)
		return nil, err
	}
	if err := unix.Fstatat(fd, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || opened.Dev != before.Dev || opened.Ino != before.Ino || opened.Dev != after.Dev || opened.Ino != after.Ino || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Size > maxSize {
		_ = unix.Close(fileFD)
		return nil, errors.New("managed file changed while it was opened")
	}
	return os.NewFile(uintptr(fileFD), abs), nil
}
