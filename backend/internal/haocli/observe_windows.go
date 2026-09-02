//go:build windows

package haocli

import (
	"errors"
	"os"
)

func statFile(path string) (FileObservation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileObservation{}, err
	}
	return FileObservation{Mode: info.Mode(), UID: -1, Owner: true, IsDir: info.IsDir(), Link: info.Mode()&os.ModeSymlink != 0}, nil
}

func diskAvailable(string) (uint64, error) {
	return 0, errors.New("disk availability probe unsupported on windows")
}

func distributionID() (string, error) { return "windows", nil }
