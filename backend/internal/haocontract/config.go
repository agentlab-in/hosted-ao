// Package haocontract implements the published hao configuration contract.
package haocontract

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

// ConfigSchemaJSON is generated from contracts/hao/v1/config.schema.json.
//
//go:generate go run ./cmd/genschema
var ConfigSchemaJSON = configSchemaJSON

// UnsupportedVersionError distinguishes a future/unknown contract version.
type UnsupportedVersionError struct{ Version any }

func (e UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported hao configuration version %v", e.Version)
}

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

// ParseConfig parses YAML and validates it against the embedded v1 schema.
// Secret-looking keys are rejected before schema validation so diagnostics can
// never echo their values, including when a future schema accidentally permits
// one.
func ParseConfig(data []byte) (map[string]any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("configuration must be a YAML mapping")
	}
	if key, ok := findSecretKey(object); ok {
		return nil, fmt.Errorf("forbidden secret-looking key %q", key)
	}
	version, exists := object["version"]
	if !exists {
		return nil, errors.New("configuration version is required")
	}
	if !isVersionOne(version) {
		return nil, UnsupportedVersionError{Version: version}
	}

	schemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		schemaErr = compiler.AddResource("config.schema.json", strings.NewReader(ConfigSchemaJSON))
		if schemaErr == nil {
			schema, schemaErr = compiler.Compile("config.schema.json")
		}
	})
	if schemaErr != nil {
		return nil, fmt.Errorf("load configuration contract: %w", schemaErr)
	}
	if err := schema.Validate(object); err != nil {
		return nil, fmt.Errorf("configuration does not match v1 contract: %w", err)
	}
	return object, nil
}

func isVersionOne(value any) bool {
	switch v := value.(type) {
	case int:
		return v == 1
	case uint64:
		return v == 1
	case float64:
		return v == 1
	case json.Number:
		return v.String() == "1"
	default:
		return false
	}
}

var secretKeyParts = []string{
	"apikey", "credential", "oauth", "passcode", "password", "privatekey", "secret", "token", "pairingstring",
}

var secretAssignmentPattern = regexp.MustCompile(`(?i)(pass(code|word)?|token|secret|credential|api[_-]?key|private[_-]?key)\s*[:=]\s*[^/\\\s,;]+`)

// IsSecretLookingKey reports whether a key may name credential material.
func IsSecretLookingKey(key string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(strings.ToLower(key))
	for _, part := range secretKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func findSecretKey(value any) (string, bool) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if IsSecretLookingKey(key) {
				return key, true
			}
			if found, ok := findSecretKey(child); ok {
				return found, true
			}
		}
	case []any:
		for _, child := range v {
			if found, ok := findSecretKey(child); ok {
				return found, true
			}
		}
	}
	return "", false
}

// Redact replaces values under secret-looking keys recursively. It is a
// defense-in-depth output boundary; valid v1 configuration contains no such
// keys.
func Redact(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			if IsSecretLookingKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = Redact(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = Redact(child)
		}
		return out
	default:
		if text, ok := value.(string); ok {
			return secretAssignmentPattern.ReplaceAllString(text, "$1=[REDACTED]")
		}
		return value
	}
}
