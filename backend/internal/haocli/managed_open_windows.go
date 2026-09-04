//go:build windows

package haocli

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func openManagedRegular(path string, maxSize int64) (*os.File, error) {
	clean := filepath.Clean(path)
	for current := clean; ; current = filepath.Dir(current) {
		pointer, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return nil, err
		}
		attrs, err := windows.GetFileAttributes(pointer)
		if err != nil {
			return nil, err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("managed path contains a reparse point")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	before, err := os.Lstat(clean)
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxSize {
		return nil, errors.New("managed file is not a bounded regular file")
	}
	file, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Size() > maxSize {
		_ = file.Close()
		return nil, errors.New("managed file changed while it was opened")
	}
	return file, nil
}
