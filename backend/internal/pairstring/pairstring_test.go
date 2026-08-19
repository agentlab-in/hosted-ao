package pairstring

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// vectors mirrors vectors.json, the cross-language golden-vector contract
// this package shares with the TypeScript parser (Task 3). Both sides test
// against the same file; do not fork it.
type vectors struct {
	Valid []struct {
		Name     string   `json:"name"`
		Addrs    []string `json:"addrs"`
		FP       string   `json:"fp"`
		Passcode string   `json:"passcode"`
		String   string   `json:"string"`
	} `json:"valid"`
	Invalid []struct {
		Name   string `json:"name"`
		String string `json:"string"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("vectors.json")
	if err != nil {
		t.Fatalf("read vectors.json: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors.json: %v", err)
	}
	return v
}

// TestBuild_ValidVectors checks that every valid vector's inputs Build to
// exactly the vector's string, and that Validate accepts that string.
func TestBuild_ValidVectors(t *testing.T) {
	v := loadVectors(t)
	for _, tc := range v.Valid {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := Build(tc.Addrs, tc.FP, tc.Passcode)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if got != tc.String {
				t.Fatalf("Build() = %q, want %q", got, tc.String)
			}
			if err := Validate(got); err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", got, err)
			}
		})
	}
}

// TestValidate_InvalidVectors checks that every invalid vector is rejected
// by the exported Validate, per the controller ruling: invalid vectors are
// asserted through Validate, not a test-only parser.
func TestValidate_InvalidVectors(t *testing.T) {
	v := loadVectors(t)
	for _, tc := range v.Invalid {
		t.Run(tc.Name, func(t *testing.T) {
			if err := Validate(tc.String); err == nil {
				t.Fatalf("Validate(%q) = nil, want error (%s)", tc.String, tc.Reason)
			}
		})
	}
}

func TestBuild_NoAddresses(t *testing.T) {
	fp := "ab"
	if _, err := Build(nil, repeat(fp, 32), "XK4M2P7Q"); err == nil {
		t.Fatal("Build with no addresses = nil error, want error")
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// TestFingerprint checks Fingerprint returns the 64-char lowercase hex
// SHA-256 of the certificate's raw DER bytes.
func TestFingerprint(t *testing.T) {
	cert := &x509.Certificate{Raw: []byte("not a real certificate, just bytes to hash")}
	sum := sha256.Sum256(cert.Raw)
	want := hex.EncodeToString(sum[:])

	got := Fingerprint(cert)
	if got != want {
		t.Fatalf("Fingerprint() = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("Fingerprint() length = %d, want 64", len(got))
	}
	for _, c := range got {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("Fingerprint() = %q contains non-lowercase-hex char %q", got, c)
		}
	}
}
