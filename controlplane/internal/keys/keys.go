// Package keys manages the control plane's EdDSA signing keys: generation,
// on-disk storage with an active and next-key rotation slot, and the public
// JWKS document served at /.well-known/jwks.json.
package keys

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// keySubdir is the directory under the service data dir holding the signing
// key files.
const keySubdir = "keys"

// keyPair is one EdDSA key pair plus a stable kid derived from the public
// key. PrivateKey and PublicKey marshal to JSON as base64 (the default for
// []byte), which is why the on-disk file must be mode 0600 and gitignored.
type keyPair struct {
	KID        string             `json:"kid"`
	PrivateKey ed25519.PrivateKey `json:"private_key"`
	PublicKey  ed25519.PublicKey  `json:"public_key"`
	CreatedAt  time.Time          `json:"created_at"`
}

// Manager holds the active and next EdDSA key pairs, persisted on disk under
// dataDir/keys. The active key signs newly issued access tokens; the next
// key is published in JWKS ahead of a future rotation so verifiers can cache
// it before it is ever used to sign.
type Manager struct {
	mu     sync.RWMutex
	dir    string
	active keyPair
	next   keyPair
}

// Load reads the active and next signing keys from dataDir/keys, generating
// and persisting whichever file is missing. On first boot neither exists, so
// both are generated. File permissions are 0600; the containing directory is
// 0700.
func Load(dataDir string) (*Manager, error) {
	dir := filepath.Join(dataDir, keySubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create keys dir: %w", err)
	}

	active, err := loadOrGenerate(filepath.Join(dir, "active.json"))
	if err != nil {
		return nil, fmt.Errorf("load active key: %w", err)
	}
	next, err := loadOrGenerate(filepath.Join(dir, "next.json"))
	if err != nil {
		return nil, fmt.Errorf("load next key: %w", err)
	}

	return &Manager{dir: dir, active: active, next: next}, nil
}

func loadOrGenerate(path string) (keyPair, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		var kp keyPair
		if err := json.Unmarshal(raw, &kp); err != nil {
			return keyPair{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return kp, nil
	}
	if !os.IsNotExist(err) {
		return keyPair{}, fmt.Errorf("read %s: %w", path, err)
	}

	kp, err := generate()
	if err != nil {
		return keyPair{}, err
	}
	if err := writeKeyPair(path, kp); err != nil {
		return keyPair{}, err
	}
	return kp, nil
}

func generate() (keyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return keyPair{}, fmt.Errorf("generate ed25519 key: %w", err)
	}
	return keyPair{
		KID:        kidFor(pub),
		PrivateKey: priv,
		PublicKey:  pub,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// kidFor derives a stable key id from the public key, so a kid can always be
// recomputed from the key material alone rather than needing its own
// separately tracked identity.
func kidFor(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

func writeKeyPair(path string, kp keyPair) error {
	raw, err := json.Marshal(kp)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Active returns the kid and private key currently used to sign access
// tokens.
func (m *Manager) Active() (kid string, priv ed25519.PrivateKey) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active.KID, m.active.PrivateKey
}

// Rotate promotes the next key to active and generates a fresh next key,
// persisting both to disk. The previously active key is discarded and no
// longer published in JWKS, so callers should only rotate when it is safe
// for tokens signed by the old active key to stop verifying (they are
// short-lived per TOKEN_CONTRACT.md, so this is bounded by the access token
// TTL plus the JWKS cache lifetime).
//
// The write order matters and is deliberate: next.json is written before
// active.json, and memory is updated only after both succeed. Writing the
// promoted key into active.json first would mean a failure on the second
// write (disk full, EPERM) left the same key in both files, so the next Load
// would publish a duplicate kid and lose the rotation slot entirely. In this
// order a failed second write leaves active.json holding the still-valid old
// active key, and the only casualty is the unused next key: signing keeps
// working and the caller can retry.
func (m *Manager) Rotate() error {
	newNext, err := generate()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := writeKeyPair(filepath.Join(m.dir, "next.json"), newNext); err != nil {
		return fmt.Errorf("persist new next key: %w", err)
	}
	if err := writeKeyPair(filepath.Join(m.dir, "active.json"), m.next); err != nil {
		return fmt.Errorf("persist promoted active key: %w", err)
	}

	m.active = m.next
	m.next = newNext
	return nil
}
