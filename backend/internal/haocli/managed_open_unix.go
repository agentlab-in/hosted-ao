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

func openManagedParent(path string) (int, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return -1, "", err
	}
	parts := strings.FieldsFunc(abs, func(r rune) bool { return r == filepath.Separator })
	if len(parts) == 0 {
		return -1, "", errors.New("managed path has no file component")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				return -1, "", openErr
			}
			return -1, "", errors.New("managed path contains an inaccessible or linked ancestor")
		}
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func managedLstat(path string) (os.FileInfo, error) {
	fd, name, err := openManagedParent(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(child), path)
	defer func() { _ = file.Close() }()
	return file.Stat()
}

func managedChmod(path string, mode os.FileMode) error {
	fd, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	child, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(child), path)
	defer func() { _ = file.Close() }()
	return file.Chmod(mode)
}

func managedMkdir(path string, mode os.FileMode) error {
	fd, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Mkdirat(fd, name, uint32(mode.Perm())); err != nil {
		return err
	}
	return unix.Fsync(fd)
}

func managedCreateExclusive(path string, mode os.FileMode) (*os.File, error) {
	fd, name, err := openManagedParent(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	child, err := unix.Openat(fd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(child), path), nil
}

func managedRename(source, destination string) error {
	sourceFD, sourceName, err := openManagedParent(source)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(sourceFD) }()
	destinationFD, destinationName, err := openManagedParent(destination)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(destinationFD) }()
	if err := unix.Renameat(sourceFD, sourceName, destinationFD, destinationName); err != nil {
		return err
	}
	if err := unix.Fsync(destinationFD); err != nil {
		return err
	}
	if sourceFD != destinationFD {
		return unix.Fsync(sourceFD)
	}
	return nil
}

func managedRemove(path string) error {
	fd, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Unlinkat(fd, name, 0); err != nil {
		return err
	}
	return unix.Fsync(fd)
}
