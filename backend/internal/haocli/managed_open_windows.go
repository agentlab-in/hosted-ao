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

const managedWindowsShareMode = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE

type managedWindowsRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   windows.Handle
	FileNameLength  uint32
	FileName        [1]uint16
}

func openManagedRegular(path string, maxSize int64) (*os.File, error) {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	handle, err := openManagedRelative(parent, name, windows.FILE_GENERIC_READ|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return nil, errors.New("managed file could not be opened without traversing a reparse point")
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSize {
		_ = file.Close()
		return nil, errors.New("managed file is not a bounded regular file")
	}
	return file, nil
}

func openManagedParent(path string) (windows.Handle, string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return 0, "", err
	}
	name := filepath.Base(abs)
	parentPath := filepath.Dir(abs)
	if name == "." || name == string(filepath.Separator) || parentPath == abs {
		return 0, "", errors.New("managed path has no file component")
	}
	handle, err := openManagedAbsolute(parentPath, windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		if isManagedWindowsNotExist(err) {
			return 0, "", os.ErrNotExist
		}
		return 0, "", errors.New("managed path contains an inaccessible or reparse ancestor")
	}
	return handle, name, nil
}

func openManagedAbsolute(path string, access uint32, options uint32) (windows.Handle, error) {
	ntPath := `\??\` + path
	if strings.HasPrefix(path, `\\`) {
		ntPath = `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	name, err := windows.NewNTUnicodeString(ntPath)
	if err != nil {
		return 0, err
	}
	attributes := managedWindowsObjectAttributes(0, name)
	return ntCreateManagedWindowsFile(attributes, access, windows.FILE_OPEN, options)
}

func openManagedRelative(parent windows.Handle, name string, access uint32, disposition uint32, options uint32) (windows.Handle, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return 0, errors.New("managed path has invalid file component")
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := managedWindowsObjectAttributes(parent, objectName)
	return ntCreateManagedWindowsFile(attributes, access, disposition, options)
}

func managedWindowsObjectAttributes(parent windows.Handle, name *windows.NTUnicodeString) *windows.OBJECT_ATTRIBUTES {
	attributes := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    name,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	return attributes
}

func ntCreateManagedWindowsFile(attributes *windows.OBJECT_ATTRIBUTES, access uint32, disposition uint32, options uint32) (windows.Handle, error) {
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err := windows.NtCreateFile(&handle, access, attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		managedWindowsShareMode, disposition, options, 0, 0)
	return handle, err
}

func managedLstat(path string) (os.FileInfo, error) {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	handle, err := openManagedRelative(parent, name, windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		if isManagedWindowsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("managed target is inaccessible or a reparse point")
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return file.Stat()
}

func managedChmod(path string, mode os.FileMode) error {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	return managedChmodAt(parent, name, path, mode)
}

func managedChmodAt(parent windows.Handle, name, path string, mode os.FileMode) error {
	handle, err := openManagedRelative(parent, name, windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return normalizeManagedWindowsError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return file.Chmod(mode)
}

func managedMkdir(path string, mode os.FileMode) error {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	return managedMkdirAt(parent, name, path, mode)
}

func managedMkdirAt(parent windows.Handle, name, path string, mode os.FileMode) error {
	handle, err := openManagedRelative(parent, name, windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|windows.FILE_WRITE_ATTRIBUTES|windows.SYNCHRONIZE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return normalizeManagedWindowsError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	return file.Chmod(mode)
}

func managedCreateExclusive(path string, mode os.FileMode) (*os.File, error) {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(parent)
	return managedCreateExclusiveAt(parent, name, path, mode)
}

func managedCreateExclusiveAt(parent windows.Handle, name, path string, mode os.FileMode) (*os.File, error) {
	handle, err := openManagedRelative(parent, name, windows.FILE_GENERIC_WRITE|windows.SYNCHRONIZE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return nil, normalizeManagedWindowsError(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func managedRename(source, destination string) error {
	sourceParent, sourceName, err := openManagedParent(source)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(sourceParent)
	destinationParent, destinationName, err := openManagedParent(destination)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(destinationParent)
	sourceHandle, err := openManagedRelative(sourceParent, sourceName, windows.DELETE|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return normalizeManagedWindowsError(err)
	}
	defer windows.CloseHandle(sourceHandle)
	return renameManagedWindowsHandle(sourceHandle, destinationParent, destinationName)
}

func renameManagedWindowsHandle(source, destinationParent windows.Handle, destinationName string) error {
	name, err := windows.UTF16FromString(destinationName)
	if err != nil {
		return err
	}
	nameLength := (len(name) - 1) * 2
	var layout managedWindowsRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.FileName)) + nameLength
	buffer := make([]byte, bufferSize)
	info := (*managedWindowsRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.ReplaceIfExists = windows.FILE_RENAME_REPLACE_IF_EXISTS
	info.RootDirectory = destinationParent
	info.FileNameLength = uint32(nameLength)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:nameLength/2:nameLength/2], name)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(source, &status, &buffer[0], uint32(bufferSize), windows.FileRenameInformation)
}

func managedRemove(path string) error {
	parent, name, err := openManagedParent(path)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(parent)
	return managedRemoveAt(parent, name)
}

func managedRemoveAt(parent windows.Handle, name string) error {
	handle, err := openManagedRelative(parent, name, windows.DELETE|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return normalizeManagedWindowsError(err)
	}
	defer windows.CloseHandle(handle)
	deleteFile := byte(1)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &deleteFile, 1, windows.FileDispositionInformation)
}

func isManagedWindowsNotExist(err error) bool {
	return errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) || errors.Is(err, windows.STATUS_OBJECT_PATH_NOT_FOUND)
}

func normalizeManagedWindowsError(err error) error {
	if isManagedWindowsNotExist(err) {
		return os.ErrNotExist
	}
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return os.ErrExist
	}
	return err
}
