package haocontract

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactSecretLookingValuesRecursively(t *testing.T) {
	input := map[string]any{
		"machine": map[string]any{"name": "box", "api_token": "do-not-print"},
		"items":   []any{map[string]any{"private-key": "also-secret"}},
	}
	got := Redact(input)
	rendered := fmt.Sprint(got)
	if strings.Contains(rendered, "do-not-print") || strings.Contains(rendered, "also-secret") || !strings.Contains(rendered, "[REDACTED]") {
		t.Fatalf("redaction failed: %v", got)
	}
}
