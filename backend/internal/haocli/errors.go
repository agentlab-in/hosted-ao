package haocli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type commandError struct {
	Code        string
	Message     string
	Operation   string
	Remediation string
	Details     map[string]any
	ExitStatus  int
	Cause       error
}

func (e commandError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + safeDiagnostic(e.Cause)
	}
	return e.Message
}

func (e commandError) Unwrap() error { return e.Cause }

type errorEnvelope struct {
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Component   string         `json:"component"`
	Operation   string         `json:"operation"`
	Remediation string         `json:"remediation"`
	Details     map[string]any `json:"details"`
}

func operationalError(operation string, err error) commandError {
	return commandError{Code: "operation_failed", Message: operation + " failed", Remediation: "check the path and permissions, then retry", Details: map[string]any{}, ExitStatus: 1, Cause: err}
}

func classifyError(err error, _ bool, operation string) commandError {
	var typed commandError
	if errors.As(err, &typed) {
		if typed.Details == nil {
			typed.Details = map[string]any{}
		}
		if typed.Operation == "" {
			typed.Operation = operation
		}
		return typed
	}
	message := err.Error()
	if strings.HasPrefix(message, "unknown command") || strings.HasPrefix(message, "requires ") {
		return commandError{Code: "invalid_usage", Message: message, Operation: operation, Remediation: "run hao --help", Details: map[string]any{}, ExitStatus: 2, Cause: err}
	}
	return commandError{Code: "operation_failed", Message: "hao command failed", Operation: operation, Remediation: "retry after checking the reported diagnostic", Details: map[string]any{"diagnostic": safeDiagnostic(err)}, ExitStatus: 1, Cause: err}
}

func emitError(w io.Writer, err commandError, jsonOutput bool) error {
	if jsonOutput {
		return json.NewEncoder(w).Encode(errorEnvelope{Code: err.Code, Message: err.Message, Component: "hao", Operation: err.Operation, Remediation: err.Remediation, Details: err.Details})
	}
	_, writeErr := fmt.Fprintf(w, "hao: %s\nremediation: %s\n", err.Error(), err.Remediation)
	return writeErr
}

func operationFromArgs(args []string) string {
	parts := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--config" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-") {
			continue
		}
		parts = append(parts, arg)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return "command"
	}
	return strings.Join(parts, " ")
}

var assignmentPattern = regexp.MustCompile(`(?i)(pass(code|word)?|token|secret|credential|api[_-]?key|private[_-]?key)\s*[:=]\s*[^\s,;]+`)

func safeDiagnostic(err error) string {
	if err == nil {
		return ""
	}
	return assignmentPattern.ReplaceAllString(err.Error(), "$1=[REDACTED]")
}
