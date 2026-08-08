package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestKeySet(t *testing.T, handler http.HandlerFunc) (*keySet, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return newKeySet(srv.URL, srv.Client(), time.Hour), &hits
}

func staticJWKS(t *testing.T, kid string) (http.HandlerFunc, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJWKS(w, map[string]*rsa.PublicKey{kid: &key.PublicKey})
	}, key
}

func TestKeySetFetchesOnceThenServesFromCache(t *testing.T) {
	h, key := staticJWKS(t, "k1")
	ks, hits := newTestKeySet(t, h)

	for i := 0; i < 3; i++ {
		got, err := ks.Key(context.Background(), "k1")
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if got.N.Cmp(key.PublicKey.N) != 0 {
			t.Fatal("returned the wrong key")
		}
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected exactly 1 fetch, got %d", n)
	}
}

func TestKeySetRefetchesAfterTTL(t *testing.T) {
	h, _ := staticJWKS(t, "k1")
	ks, hits := newTestKeySet(t, h)

	now := time.Now()
	ks.now = func() time.Time { return now }

	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("first: %v", err)
	}
	now = now.Add(2 * time.Hour) // past the 1h TTL
	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("second: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected 2 fetches across the TTL boundary, got %d", n)
	}
}

func TestKeySetRefetchesOnceForUnknownKid(t *testing.T) {
	h, _ := staticJWKS(t, "k1")
	ks, hits := newTestKeySet(t, h)

	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if _, err := ks.Key(context.Background(), "rotated-in"); err == nil {
		t.Fatal("expected an error for a kid the issuer does not publish")
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected 1 warm + 1 refresh = 2 fetches, got %d", n)
	}
}

// An unknown kid must not turn every request into a round trip to Cognito.
func TestKeySetRateLimitsUnknownKidRefresh(t *testing.T) {
	h, _ := staticJWKS(t, "k1")
	ks, hits := newTestKeySet(t, h)

	now := time.Now()
	ks.now = func() time.Time { return now }

	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	for i := 0; i < 25; i++ {
		if _, err := ks.Key(context.Background(), "garbage-kid"); err == nil {
			t.Fatal("expected an error")
		}
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected the refresh to be rate limited to 2 total fetches, got %d", n)
	}

	now = now.Add(minRefreshInterval + time.Second)
	if _, err := ks.Key(context.Background(), "garbage-kid"); err == nil {
		t.Fatal("expected an error")
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("expected one more fetch after the interval elapsed, got %d", n)
	}
}

func TestKeySetSurfacesFetchFailureWithoutPoisoningCache(t *testing.T) {
	var fail atomic.Bool
	h, _ := staticJWKS(t, "k1")
	ks, _ := newTestKeySet(t, func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h(w, r)
	})

	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	fail.Store(true)
	now := time.Now()
	ks.now = func() time.Time { return now.Add(2 * time.Hour) } // force staleness
	if _, err := ks.Key(context.Background(), "k1"); err == nil {
		t.Fatal("expected the upstream 500 to surface")
	}

	// The previously cached key must still be intact once the issuer recovers.
	fail.Store(false)
	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("cache was poisoned by the failed refresh: %v", err)
	}
}

func TestKeySetEmptyDocumentDoesNotDiscardGoodKeys(t *testing.T) {
	var empty atomic.Bool
	h, _ := staticJWKS(t, "k1")
	ks, _ := newTestKeySet(t, func(w http.ResponseWriter, r *http.Request) {
		if empty.Load() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
			return
		}
		h(w, r)
	})

	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("warm cache: %v", err)
	}

	empty.Store(true)
	now := time.Now()
	ks.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := ks.Key(context.Background(), "k1"); err == nil {
		t.Fatal("expected an error when the issuer returns no usable keys")
	}

	empty.Store(false)
	if _, err := ks.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("good keys were discarded by an empty document: %v", err)
	}
}

func TestKeySetConcurrentMissesCauseOneFetch(t *testing.T) {
	h, _ := staticJWKS(t, "k1")
	ks, hits := newTestKeySet(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond) // widen the race window
		h(w, r)
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ks.Key(context.Background(), "k1")
		}()
	}
	wg.Wait()

	if n := hits.Load(); n > 2 {
		t.Fatalf("concurrent misses should collapse into ~1 fetch, got %d", n)
	}
}
