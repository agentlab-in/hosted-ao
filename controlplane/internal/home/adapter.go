package home

import (
	"context"
	"time"
)

// Adapt wraps list and revoke functions as a Machines implementation so main
// can bridge device.Service without home importing device.
func Adapt(
	list func(ctx context.Context, accountID string) ([]Machine, error),
	revoke func(ctx context.Context, accountID, machineID string, now time.Time) (bool, error),
) Machines {
	return machinesFuncs{list: list, revoke: revoke}
}

type machinesFuncs struct {
	list   func(ctx context.Context, accountID string) ([]Machine, error)
	revoke func(ctx context.Context, accountID, machineID string, now time.Time) (bool, error)
}

func (m machinesFuncs) ListMachines(ctx context.Context, accountID string) ([]Machine, error) {
	return m.list(ctx, accountID)
}

func (m machinesFuncs) RevokeMachine(ctx context.Context, accountID, machineID string, now time.Time) (bool, error) {
	return m.revoke(ctx, accountID, machineID, now)
}
