package vmgateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// pairCertFileName and pairKeyFileName are the two files
	// LoadOrCreatePairCertificate persists under a pair-mode Config.CertDir.
	pairCertFileName = "cert.pem"
	pairKeyFileName  = "key.pem"

	// pairCertValidity is how long a freshly generated pair-mode certificate
	// is valid for. Long by design: this is the certificate a client pins by
	// fingerprint (see docs/adr/0003-pair-mode-gateway.md), so its expiry is
	// the one thing that WOULD force every paired client to re-pair on a
	// schedule, the exact failure LoadOrCreatePairCertificate otherwise
	// exists to prevent. Ten years comfortably outlives any realistic
	// upgrade cadence for a box that is not otherwise being renewed by
	// anything; a deliberate rotation (a fresh state root, or a future
	// rotate command) is how this is ever meant to change.
	pairCertValidity = 10 * 365 * 24 * time.Hour

	// pairCertCommonName labels the certificate's Subject; it carries no
	// security meaning because pair-mode clients pin the certificate's
	// fingerprint (see PairFingerprint), never a name in it.
	pairCertCommonName = "ao-pair-gateway"
)

// PairIPsFromAddresses parses the address hints a pairing string advertises
// (bare "1.2.3.4" or "1.2.3.4:443", bracketed or not for IPv6) into the
// []net.IP a freshly generated pair certificate should carry as IP Subject
// Alternative Names. Malformed or unparsable entries are skipped rather than
// fatal: the certificate's identity is its fingerprint, not its SANs, so a
// hint that cannot be turned into an IP SAN is never a reason to refuse
// provisioning.
func PairIPsFromAddresses(addrs []string) []net.IP {
	var ips []net.IP
	for _, addr := range addrs {
		host := strings.TrimSpace(addr)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			ips = append(ips, ip)
		}
	}
	return ips
}

// LoadOrCreatePairCertificate returns the pair-mode gateway's TLS
// certificate, persisted under dir as cert.pem and key.pem. The first call
// (neither file exists) generates a new self-signed key pair and certificate
// and persists them. Every subsequent call, including across process
// restarts and binary upgrades, loads and returns the exact same certificate
// rather than generating a new one.
//
// ips, when non-empty, are carried as the certificate's IP Subject
// Alternative Names so the certificate is valid for the bare-IP addresses a
// paired client connects to. Chromium (the Electron desktop app) enforces
// that a certificate presented for a bare IP has that IP in its SAN, and
// curl/OpenSSL does not, so a certificate with no IP SANs is exactly the
// shape that works from curl and fails from the desktop (see
// docs/adr/0003-pair-mode-gateway.md). The caller that mints the certificate
// at provisioning time passes the address hints the pairing string
// advertises; the gateway process itself passes none, because by the time it
// serves the certificate already exists on disk.
//
// That reuse is the whole point: a client that has pinned this certificate's
// fingerprint (trust-on-first-use, see docs/adr/0003-pair-mode-gateway.md)
// must never see it change under a running box, because a routine restart
// producing a new certificate would be indistinguishable, from the client's
// side, from an attacker presenting a different one. If exactly one of the
// two files is present, or the pair fails to parse, that is treated as
// corruption, not "first run": returning a fresh certificate in that
// situation would be exactly the silent rotation this function exists to
// prevent, so it fails loudly instead and leaves recovery (deleting both
// files, which does force a re-pair) to a deliberate operator action.
func LoadOrCreatePairCertificate(dir string, ips ...net.IP) (tls.Certificate, error) {
	certPath := filepath.Join(dir, pairCertFileName)
	keyPath := filepath.Join(dir, pairKeyFileName)
	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)

	switch {
	case certExists && keyExists:
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf(
				"load existing pair certificate from %s: %w (deleting %s and %s regenerates it, which forces every paired client to re-pair)",
				dir, err, pairCertFileName, pairKeyFileName)
		}
		return cert, nil
	case certExists || keyExists:
		return tls.Certificate{}, fmt.Errorf(
			"pair certificate directory %s has only one of %s/%s, which looks like an interrupted write: remove both files to regenerate (this forces every paired client to re-pair)",
			dir, pairCertFileName, pairKeyFileName)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create pair cert dir: %w", err)
	}
	certDER, keyDER, err := generatePairCertificate(ips)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate pair certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Key first, then certificate: if a crash lands between the two writes,
	// the mismatched-pair branch above catches it on the next start rather
	// than silently generating a new certificate.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write pair certificate key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write pair certificate: %w", err)
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

// generatePairCertificate creates a new ECDSA P-256 key and a self-signed
// certificate over it, returning both DER-encoded. ips are the address hints
// carried as IP Subject Alternative Names (see LoadOrCreatePairCertificate);
// they make the certificate Chromium-valid for the bare IPs a paired client
// connects to.
func generatePairCertificate(ips []net.IP) (certDER, keyDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate private key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial number: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: pairCertCommonName},
		// Backdated an hour to tolerate a box whose clock has not synced yet
		// at first boot; NotBefore in the future would make the certificate
		// invalid until the clock catches up.
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(pairCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// The IP SANs are what make this certificate valid to Chromium for a
		// bare-IP connection. A bare IP is not a name, so a DNS SAN can never
		// match it; only an IP SAN can, and Chromium enforces that where
		// curl/OpenSSL does not. The certificate's identity remains its
		// fingerprint (PairFingerprint), never a name or address in the SAN.
		IPAddresses: ips,
	}
	certDER, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err = x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	return certDER, keyDER, nil
}

// PairFingerprint renders the SHA-256 fingerprint of a pair-mode
// certificate's leaf (its DER bytes, cert.Certificate[0]) as uppercase hex
// octets separated by colons, e.g. "07:CA:9F:3E:B2:11:...".
//
// This exact rendering is the pairing contract and three components must
// keep producing byte-identical output for the same certificate: the box
// prints it during setup (batch 3, `ao setup-vm --pair`), the CLI can print
// it again later, and the desktop app (batch 2) renders the fingerprint it
// receives over TLS in this same format so a person can compare the two
// strings by eye during trust-on-first-use pairing (see
// docs/adr/0003-pair-mode-gateway.md). Do not change the case, the grouping,
// the separator, or the hash algorithm without updating all three.
func PairFingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("pair certificate fingerprint: certificate has no DER bytes")
	}
	sum := sha256.Sum256(cert.Certificate[0])
	octets := make([]string, len(sum))
	for i, b := range sum {
		octets[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(octets, ":"), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// PairCertExists reports whether dir already holds a persisted pair-mode
// certificate, without loading or creating one. This is the read-only
// counterpart to LoadOrCreatePairCertificate for callers that must never
// risk minting a new certificate just to answer a yes/no question (`ao pair
// show`, which only ever reads what is on disk, and `ao vm rotate-passcode`,
// which promises the pinned certificate is unaffected by a rotation): both
// gate their own certificate load behind this instead of calling
// LoadOrCreatePairCertificate directly, which would silently generate a
// fresh certificate against an empty dir. Exported so those callers never
// have to duplicate pairCertFileName's value themselves.
func PairCertExists(dir string) bool {
	return fileExists(filepath.Join(dir, pairCertFileName))
}
