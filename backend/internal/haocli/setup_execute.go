package haocli

import (
	"context"
	"errors"
)

// SetupExecutionOptions carries consent and interaction policy into execution.
type SetupExecutionOptions struct {
	NonInteractive bool `json:"nonInteractive"`
}

// SetupExecutionResult is the stable, non-secret execution summary.
type SetupExecutionResult struct {
	Status    string   `json:"status"`
	Completed []string `json:"completed"`
	Rollback  []string `json:"rollback"`
	Retry     string   `json:"retry"`
}

func executeSetupPlan(context.Context, SetupPlan, string, SetupExecutionOptions) (SetupExecutionResult, error) {
	return SetupExecutionResult{}, errors.New("setup executor is not implemented")
}
