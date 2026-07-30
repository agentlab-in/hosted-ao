package vmgateway

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// KeySet is a JWKS resolved to usable Ed25519 public keys, indexed by kid.
type KeySet struct {
	byKID map[string]ed25519.PublicKey
}

// jwk is one entry of a JWK Set, per RFC 7517/8037. Only OKP/Ed25519 keys
// (the only kind the control plane signs with, per TOKEN_CONTRACT.md) are
// usable; any other kty/crv is skipped rather than rejected outright, so a
// future rotation to an additional key type does not break parsing of the
// keys ao-vm-serve does understand.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
}

type jwkDocument struct {
	Keys []jwk `json:"keys"`
}

// parseKeySet decodes a JWKS document into usable Ed25519 keys. It returns an
// error when the document is not valid JSON, or contains zero usable keys —
// a JWKS that currently publishes only key types ao-vm-serve cannot use must
// not silently verify nothing without saying why.
func parseKeySet(data []byte) (*KeySet, error) {
	var doc jwkDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse jwks: %w", err)
	}
	ks := &KeySet{byKID: make(map[string]ed25519.PublicKey, len(doc.Keys))}
	for _, k := range doc.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		ks.byKID[k.Kid] = ed25519.PublicKey(raw)
	}
	if len(ks.byKID) == 0 {
		return nil, errors.New("jwks contains no usable Ed25519 keys")
	}
	return ks, nil
}

// candidates returns every key worth trying a signature against: the one
// matching kid if the header carried a kid this set recognises, otherwise
// every key in the set. There are at most two in practice — the active key
// and the next-key rotation slot, per the spec's "Control plane" section — so
// trying all of them is cheap. kid is a lookup hint, not the security
// boundary; ed25519.Verify against the returned candidates is.
func (ks *KeySet) candidates(kid string) []ed25519.PublicKey {
	if kid != "" {
		if k, ok := ks.byKID[kid]; ok {
			return []ed25519.PublicKey{k}
		}
	}
	out := make([]ed25519.PublicKey, 0, len(ks.byKID))
	for _, k := range ks.byKID {
		out = append(out, k)
	}
	return out
}

// JWKSCache fetches and caches the control plane's published signing keys. It
// refreshes at most once per ttl. A refresh that fails after a keyset has
// already been cached serves the stale keyset instead (stale-if-error), so a
// brief control-plane outage does not disconnect working users, per
// TOKEN_CONTRACT.md. A refresh that fails before any keyset has ever been
// cached fails closed: Get returns the error and callers must treat every
// token as unverifiable.
type JWKSCache struct {
	url    string
	ttl    time.Duration
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	keys      *KeySet
	fetchedAt time.Time
}

// NewJWKSCache builds a cache for url with the spec's 1 hour TTL. client
// defaults to one with a bounded timeout when nil.
func NewJWKSCache(url string, client *http.Client) *JWKSCache {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &JWKSCache{url: url, ttl: time.Hour, client: client, now: time.Now}
}

// Get returns the current key set, refreshing it first if the cache is empty
// or older than the TTL. See the JWKSCache doc comment for the
// stale-if-error and fail-closed behavior.
func (c *JWKSCache) Get(ctx context.Context) (*KeySet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.keys != nil && c.now().Sub(c.fetchedAt) < c.ttl {
		return c.keys, nil
	}

	fresh, err := c.fetch(ctx)
	if err != nil {
		if c.keys != nil {
			return c.keys, nil
		}
		return nil, err
	}
	c.keys = fresh
	c.fetchedAt = c.now()
	return c.keys, nil
}

func (c *JWKSCache) fetch(ctx context.Context) (*KeySet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build jwks request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch jwks: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read jwks response: %w", err)
	}
	return parseKeySet(body)
}
