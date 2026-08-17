package vmgateway

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// TestLoadOrCreatePairCertificate_ReusesOnSecondStart is the single most
// important test in this file. A pair-mode client pins the certificate's
// fingerprint on first connection (trust-on-first-use); if a routine restart
// or upgrade generated a new certificate, the fingerprint would change and
// every paired client would either be locked out or, worse, would have no way
// to tell a routine restart apart from an attacker presenting a different
// certificate. See docs/adr/0003-pair-mode-gateway.md.
func TestLoadOrCreatePairCertificate_ReusesOnSecondStart(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreatePairCertificate(dir)
	if err != nil {
		t.Fatalf("first start: LoadOrCreatePairCertificate: %v", err)
	}
	firstFP, err := PairFingerprint(first)
	if err != nil {
		t.Fatalf("PairFingerprint(first): %v", err)
	}

	// A second call models a process restart: the same dir, a fresh call,
	// nothing in memory carried over.
	second, err := LoadOrCreatePairCertificate(dir)
	if err != nil {
		t.Fatalf("second start: LoadOrCreatePairCertificate: %v", err)
	}
	secondFP, err := PairFingerprint(second)
	if err != nil {
		t.Fatalf("PairFingerprint(second): %v", err)
	}

	if !bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatal("second start generated a different certificate than the first: this is exactly the spurious rotation that is indistinguishable from an attack to a client that has pinned the old fingerprint")
	}
	if firstFP != secondFP {
		t.Fatalf("fingerprint changed across restarts: first %s, second %s", firstFP, secondFP)
	}

	// A third call, modelling a binary upgrade (new process, same state
	// root), must still agree.
	third, err := LoadOrCreatePairCertificate(dir)
	if err != nil {
		t.Fatalf("third start: LoadOrCreatePairCertificate: %v", err)
	}
	thirdFP, err := PairFingerprint(third)
	if err != nil {
		t.Fatalf("PairFingerprint(third): %v", err)
	}
	if thirdFP != firstFP {
		t.Fatalf("fingerprint drifted on a third start: %s, want %s", thirdFP, firstFP)
	}
}

func TestLoadOrCreatePairCertificate_GeneratesOnFirstStart(t *testing.T) {
	dir := t.TempDir()

	cert, err := LoadOrCreatePairCertificate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("expected a non-empty certificate chain")
	}

	certPath := filepath.Join(dir, pairCertFileName)
	keyPath := filepath.Join(dir, pairKeyFileName)
	if _, err := os.Stat(certPath); err != nil {
		t.Errorf("expected %s to be persisted: %v", certPath, err)
	}
	if info, err := os.Stat(keyPath); err != nil {
		t.Errorf("expected %s to be persisted: %v", keyPath, err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file permissions = %o, want 0600 (it is a private key)", perm)
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse generated certificate: %v", err)
	}
	if leaf.IsCA {
		t.Error("pair-mode certificate must be a leaf certificate, not a CA")
	}
	validFor := leaf.NotAfter.Sub(leaf.NotBefore)
	if validFor < 9*365*24*time.Hour {
		t.Errorf("certificate validity = %s, want a long-lived certificate (see pairCertValidity)", validFor)
	}
	found := false
	for _, use := range leaf.ExtKeyUsage {
		if use == x509.ExtKeyUsageServerAuth {
			found = true
		}
	}
	if !found {
		t.Error("certificate must be usable for TLS server auth")
	}
}

func TestLoadOrCreatePairCertificate_MismatchedFilesIsError(t *testing.T) {
	dir := t.TempDir()
	// Simulate an interrupted write: only one of the two files present.
	if err := os.WriteFile(filepath.Join(dir, pairCertFileName), []byte("not a real cert"), 0o644); err != nil {
		t.Fatalf("write partial cert file: %v", err)
	}

	if _, err := LoadOrCreatePairCertificate(dir); err == nil {
		t.Fatal("expected an error when only cert.pem is present, not a silent regeneration")
	}
}

func TestLoadOrCreatePairCertificate_CorruptFilesIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pairCertFileName), []byte("not a real cert"), 0o644); err != nil {
		t.Fatalf("write corrupt cert file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pairKeyFileName), []byte("not a real key"), 0o600); err != nil {
		t.Fatalf("write corrupt key file: %v", err)
	}

	if _, err := LoadOrCreatePairCertificate(dir); err == nil {
		t.Fatal("expected an error for an unparseable existing certificate pair, not a silent regeneration")
	}
}

// TestPairFingerprint_Format pins the exact rendering documented on
// PairFingerprint: 32 uppercase hex octets (a SHA-256 digest) separated by
// colons. Two later tasks (the desktop pinning UI and the CLI/setup printer)
// depend on this exact string shape matching byte for byte.
func TestPairFingerprint_Format(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreatePairCertificate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate: %v", err)
	}
	fp, err := PairFingerprint(cert)
	if err != nil {
		t.Fatalf("PairFingerprint: %v", err)
	}

	pattern := regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){31}$`)
	if !pattern.MatchString(fp) {
		t.Fatalf("fingerprint %q does not match the documented format (32 uppercase hex octets separated by colons)", fp)
	}
}

func TestPairFingerprint_EmptyCertificateErrors(t *testing.T) {
	if _, err := PairFingerprint(tls.Certificate{}); err == nil {
		t.Fatal("expected an error for a certificate with no DER bytes")
	}
}

func TestPairFingerprint_DifferentDirsProduceDifferentFingerprints(t *testing.T) {
	certA, err := LoadOrCreatePairCertificate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate (a): %v", err)
	}
	certB, err := LoadOrCreatePairCertificate(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreatePairCertificate (b): %v", err)
	}
	fpA, err := PairFingerprint(certA)
	if err != nil {
		t.Fatalf("PairFingerprint(a): %v", err)
	}
	fpB, err := PairFingerprint(certB)
	if err != nil {
		t.Fatalf("PairFingerprint(b): %v", err)
	}
	if fpA == fpB {
		t.Fatal("two independently generated certificates produced the same fingerprint; the generator is not actually randomizing the key/serial")
	}
}
