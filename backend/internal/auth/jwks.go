package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

const (
	defaultJWKSTTL = time.Hour
	// minRefreshInterval bounds how often an unrecognised `kid` may force a
	// network fetch. Without it, a caller presenting garbage key ids would turn
	// every request into a round trip to Cognito.
	minRefreshInterval = 30 * time.Second
	maxJWKSBytes       = 1 << 20
)

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// keySet caches the issuer's public keys.
type keySet struct {
	url string
	hc  *http.Client
	ttl time.Duration
	now func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	// lastMissRefresh tracks only refreshes triggered by an unrecognised kid
	// against an otherwise fresh cache. A successful scheduled fetch must not
	// consume the miss budget, or the first genuine key rotation goes unseen
	// for a full interval.
	lastMissRefresh time.Time
	// gen increments on every successful refresh so that callers who queued
	// behind refreshMu can tell whether their refresh already happened.
	gen uint64

	refreshMu sync.Mutex
}

func newKeySet(url string, hc *http.Client, ttl time.Duration) *keySet {
	return &keySet{
		url:  url,
		hc:   hc,
		ttl:  ttl,
		now:  time.Now,
		keys: map[string]*rsa.PublicKey{},
	}
}

// Key returns the public key for kid, fetching or refreshing the key set when
// the cache is stale or does not contain it.
func (k *keySet) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := k.cached(kid); ok {
		return key, nil
	}
	gen, ok := k.mayRefresh()
	if !ok {
		return nil, fmt.Errorf("auth: unknown key id %q", kid)
	}
	if err := k.refresh(ctx, gen); err != nil {
		return nil, err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if key, ok := k.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("auth: unknown key id %q", kid)
}

func (k *keySet) cached(kid string) (*rsa.PublicKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.now().Sub(k.fetchedAt) >= k.ttl {
		return nil, false
	}
	key, ok := k.keys[kid]
	return key, ok
}

// mayRefresh reports whether a refresh is permitted right now and returns the
// generation the caller observed. A stale cache always permits one; a fresh
// cache missing the kid is rate limited so that garbage key ids cannot turn
// every request into a round trip to the issuer.
func (k *keySet) mayRefresh() (uint64, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if now.Sub(k.fetchedAt) >= k.ttl {
		return k.gen, true
	}
	if now.Sub(k.lastMissRefresh) < minRefreshInterval {
		return k.gen, false
	}
	k.lastMissRefresh = now
	return k.gen, true
}

func (k *keySet) generation() uint64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.gen
}

func (k *keySet) refresh(ctx context.Context, seenGen uint64) error {
	// Serialise refreshes so a burst of concurrent misses causes one fetch.
	k.refreshMu.Lock()
	defer k.refreshMu.Unlock()

	// Another goroutine refreshed while we waited for the lock; its result is
	// as good as ours would have been.
	if k.generation() != seenGen {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return fmt.Errorf("auth: build jwks request: %w", err)
	}
	resp, err := k.hc.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: fetch jwks: status %d", resp.StatusCode)
	}

	var doc jwksDoc
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&doc); err != nil {
		return fmt.Errorf("auth: decode jwks: %w", err)
	}

	parsed := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, key := range doc.Keys {
		if key.Kty != "RSA" || key.Kid == "" {
			continue
		}
		pub, err := key.rsaPublicKey()
		if err != nil {
			// One malformed entry must not discard the rest of the set.
			continue
		}
		parsed[key.Kid] = pub
	}
	if len(parsed) == 0 {
		// Leave the previous set in place rather than poisoning the cache with
		// an empty document.
		return fmt.Errorf("auth: jwks contained no usable RSA keys")
	}

	k.mu.Lock()
	k.keys = parsed
	k.fetchedAt = k.now()
	k.gen++
	k.mu.Unlock()
	return nil
}

func (j jwk) rsaPublicKey() (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(nBytes) == 0 || len(eBytes) == 0 {
		return nil, fmt.Errorf("empty modulus or exponent")
	}
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() || e.Int64() < 3 {
		return nil, fmt.Errorf("implausible exponent")
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(e.Int64()),
	}, nil
}
