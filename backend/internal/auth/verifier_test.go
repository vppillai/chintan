package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testKID      = "test-key-1"
	testClientID = "client-abc"
)

type jwksFixture struct {
	key    *rsa.PrivateKey
	server *httptest.Server
	issuer string
	hits   *atomic.Int32
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		writeJWKS(w, map[string]*rsa.PublicKey{testKID: &key.PublicKey})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &jwksFixture{key: key, server: srv, issuer: srv.URL, hits: &hits}
}

func writeJWKS(w http.ResponseWriter, keys map[string]*rsa.PublicKey) {
	doc := jwksDoc{}
	for kid, pub := range keys {
		doc.Keys = append(doc.Keys, jwk{
			Kid: kid,
			Kty: "RSA",
			Alg: "RS256",
			N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// verifierFor builds a verifier pointed at the fixture. The fixture's issuer is
// an httptest URL, which is why NewCognitoVerifier permits http://127.0.0.1.
func (f *jwksFixture) verifier(t *testing.T) *CognitoVerifier {
	t.Helper()
	v, err := NewCognitoVerifier(f.issuer, testClientID, f.server.Client())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}

type tokenOpts struct {
	issuer   string
	sub      string
	tokenUse string
	aud      string
	clientID string
	exp      time.Time
	kid      string
	signWith *rsa.PrivateKey
	method   jwt.SigningMethod
}

func (f *jwksFixture) mint(t *testing.T, o tokenOpts) string {
	t.Helper()
	if o.issuer == "" {
		o.issuer = f.issuer
	}
	if o.sub == "" {
		o.sub = "user-123"
	}
	if o.exp.IsZero() {
		o.exp = time.Now().Add(time.Hour)
	}
	if o.kid == "" {
		o.kid = testKID
	}
	if o.signWith == nil {
		o.signWith = f.key
	}
	if o.method == nil {
		o.method = jwt.SigningMethodRS256
	}

	claims := jwt.MapClaims{
		"iss": o.issuer,
		"sub": o.sub,
		"exp": jwt.NewNumericDate(o.exp),
		"iat": jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}
	if o.tokenUse != "" {
		claims["token_use"] = o.tokenUse
	}
	if o.aud != "" {
		claims["aud"] = o.aud
	}
	if o.clientID != "" {
		claims["client_id"] = o.clientID
	}

	tok := jwt.NewWithClaims(o.method, claims)
	tok.Header["kid"] = o.kid

	var signed string
	var err error
	if o.method == jwt.SigningMethodNone {
		signed, err = tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	} else {
		signed, err = tok.SignedString(o.signWith)
	}
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestVerifyAcceptsValidIDToken(t *testing.T) {
	f := newJWKSFixture(t)
	v := f.verifier(t)

	raw := f.mint(t, tokenOpts{tokenUse: "id", aud: testClientID, sub: "user-123"})
	id, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id.UserID != "user-123" {
		t.Fatalf("UserID=%q", id.UserID)
	}
	if id.TenantID != "user-123" {
		t.Fatalf("TenantID=%q, must default to the subject", id.TenantID)
	}
}

func TestVerifyAcceptsValidAccessToken(t *testing.T) {
	f := newJWKSFixture(t)
	v := f.verifier(t)

	raw := f.mint(t, tokenOpts{tokenUse: "access", clientID: testClientID})
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newJWKSFixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	cases := []struct {
		name string
		opts tokenOpts
	}{
		{"alg none", tokenOpts{tokenUse: "id", aud: testClientID, method: jwt.SigningMethodNone}},
		{"signed by a foreign key", tokenOpts{tokenUse: "id", aud: testClientID, signWith: other}},
		{"expired", tokenOpts{tokenUse: "id", aud: testClientID, exp: time.Now().Add(-2 * time.Hour)}},
		{"issuer mismatch", tokenOpts{tokenUse: "id", aud: testClientID, issuer: "https://evil.example.com/pool"}},
		{"audience mismatch on id token", tokenOpts{tokenUse: "id", aud: "some-other-client"}},
		{"id token with no audience", tokenOpts{tokenUse: "id"}},
		{"client_id mismatch on access token", tokenOpts{tokenUse: "access", clientID: "some-other-client"}},
		{"missing token_use", tokenOpts{aud: testClientID}},
		{"unexpected token_use", tokenOpts{tokenUse: "refresh", aud: testClientID}},
		{"unknown kid", tokenOpts{tokenUse: "id", aud: testClientID, kid: "not-in-jwks"}},
		{"empty subject", tokenOpts{tokenUse: "id", aud: testClientID, sub: " "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh verifier per case so one case's cache state cannot mask another's.
			v := f.verifier(t)
			raw := f.mint(t, tc.opts)
			id, err := v.Verify(context.Background(), raw)
			if err == nil {
				t.Fatalf("expected rejection, got identity %+v", id)
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("error must wrap ErrUnauthenticated, got %v", err)
			}
		})
	}
}

func TestVerifyRejectsEmptyToken(t *testing.T) {
	f := newJWKSFixture(t)
	v := f.verifier(t)
	if _, err := v.Verify(context.Background(), "   "); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("got %v", err)
	}
}

func TestNewCognitoVerifierRequiresConfiguration(t *testing.T) {
	if _, err := NewCognitoVerifier("", "client", nil); err == nil {
		t.Fatal("expected error for empty issuer")
	}
	if _, err := NewCognitoVerifier("https://issuer.example", "", nil); err == nil {
		t.Fatal("expected error for empty client id")
	}
	if _, err := NewCognitoVerifier("http://issuer.example", "client", nil); err == nil {
		t.Fatal("expected error for non-https issuer")
	}
}

func TestNewCognitoIssuer(t *testing.T) {
	got := NewCognitoIssuer("us-west-2", "us-west-2_abc123")
	want := "https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if NewCognitoIssuer("", "pool") != "" || NewCognitoIssuer("region", "") != "" {
		t.Fatal("expected empty issuer when either input is missing")
	}
}
