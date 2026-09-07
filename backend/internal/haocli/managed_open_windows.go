//go:build windows

package haocli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openManagedRegular(path string, maxSize int64) (*os.File, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	ntPath := `\??\` + abs
	if strings.HasPrefix(abs, `\\`) {
		ntPath = `\??\UNC\` + strings.TrimPrefix(abs, `\\`)
	}
	name, err := windows.NewNTUnicodeString(ntPath)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0); err != nil {
		return nil, errors.New("managed file could not be opened without traversing a reparse point")
	}
	file := os.NewFile(uintptr(handle), abs)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		_ = file.Close()
		return nil, errors.New("managed file is not a bounded regular file")
	}
	return file, nil
}

func managedLstat(path string) (os.FileInfo, error) {
	if err := ensureNoSymlinkAncestors(path); err != nil {
		return nil, err
	}
	return os.Lstat(path)
}

func managedChmod(path string, mode os.FileMode) error {
	if _, err := managedLstat(path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func managedMkdir(path string, mode os.FileMode) error {
	if err := ensureNoSymlinkAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	return os.Mkdir(path, mode)
}

func managedCreateExclusive(path string, mode os.FileMode) (*os.File, error) {
	if err := ensureNoSymlinkAncestors(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
}

func managedRename(source, destination string) error {
	if err := ensureNoSymlinkAncestors(filepath.Dir(source)); err != nil {
		return err
	}
	if err := ensureNoSymlinkAncestors(filepath.Dir(destination)); err != nil {
		return err
	}
	return os.Rename(source, destination)
}

func managedRemove(path string) error {
	if err := ensureNoSymlinkAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	return os.Remove(path)
}
