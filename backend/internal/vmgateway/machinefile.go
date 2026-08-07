package vmgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// MachineFile is the shape of ~/.ao/hosted/machine.json, written by `ao setup-vm`
// once a machine is bound to an account (see the "ao setup-vm" section of
// docs/superpowers/specs/2026-07-29-hosted-ao-v1-accounts-and-machines.md).
// ao setup-vm itself is out of scope here; this type only defines what `ao vm
// serve` reads.
type MachineFile struct {
	MachineID string    `json:"machineId"`
	AccountID string    `json:"accountId"`
	PublicURL string    `json:"publicUrl"`
	IssuedAt  time.Time `json:"issuedAt"`
}

// ReadMachineFile loads machine.json from path. A missing file returns
// (nil, nil): that is the normal "not bound yet" state the caller must
// tolerate, not an error, mirroring runfile.Read.
func ReadMachineFile(path string) (*MachineFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read machine file: %w", err)
	}
	var mf MachineFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse machine file %s: %w", path, err)
	}
	return &mf, nil
}
