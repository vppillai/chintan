package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrUnauthenticated is returned for every verification failure.
//
// Callers map it to 401 without inspecting the cause. The wrapped detail is for
// logs only: telling a caller which specific claim failed is a probing oracle.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// Verifier turns a raw bearer token into an Identity.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Identity, error)
}

type cognitoClaims struct {
	jwt.RegisteredClaims
	TokenUse string `json:"token_use"`
	ClientID string `json:"client_id"`
}

// CognitoVerifier validates RS256 tokens issued by one Cognito user pool.
type CognitoVerifier struct {
	issuer   string
	clientID string
	keys     *keySet
	parser   *jwt.Parser
}

// NewCognitoVerifier builds a verifier for the given issuer and app client.
//
// issuer is the Cognito user pool issuer URL, e.g.
// https://cognito-idp.us-west-2.amazonaws.com/us-west-2_abc123
func NewCognitoVerifier(issuer, clientID string, hc *http.Client) (*CognitoVerifier, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	clientID = strings.TrimSpace(clientID)
	if issuer == "" {
		return nil, errors.New("auth: issuer is required")
	}
	if clientID == "" {
		return nil, errors.New("auth: client id is required")
	}
	if !strings.HasPrefix(issuer, "https://") && !strings.HasPrefix(issuer, "http://127.0.0.1") {
		return nil, fmt.Errorf("auth: issuer must be https: %q", issuer)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	return &CognitoVerifier{
		issuer:   issuer,
		clientID: clientID,
		keys:     newKeySet(issuer+"/.well-known/jwks.json", hc, defaultJWKSTTL),
		parser: jwt.NewParser(
			// Pinned explicitly. Relying on library defaults is how "alg: none"
			// and HMAC-key-confusion bugs get shipped.
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second),
		),
	}, nil
}

// NewCognitoIssuer builds the issuer URL from a region and user pool id.
func NewCognitoIssuer(region, userPoolID string) string {
	region = strings.TrimSpace(region)
	userPoolID = strings.TrimSpace(userPoolID)
	if region == "" || userPoolID == "" {
		return ""
	}
	return fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", region, userPoolID)
}

// Verify checks the signature and every claim the pool guarantees, then returns
// the tenant-scoped identity.
func (v *CognitoVerifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrUnauthenticated)
	}

	var claims cognitoClaims
	token, err := v.parser.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("token has no kid header")
		}
		return v.keys.Key(ctx, kid)
	})
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrUnauthenticated, err)
	}
	if !token.Valid {
		return Identity{}, fmt.Errorf("%w: token rejected", ErrUnauthenticated)
	}

	// Cognito puts the app client in `aud` on ID tokens and `client_id` on
	// access tokens. Accepting either without checking token_use would let an
	// access token satisfy an aud check it was never subject to.
	switch claims.TokenUse {
	case "id":
		if !audienceContains(claims.Audience, v.clientID) {
			return Identity{}, fmt.Errorf("%w: audience mismatch", ErrUnauthenticated)
		}
	case "access":
		if claims.ClientID != v.clientID {
			return Identity{}, fmt.Errorf("%w: client id mismatch", ErrUnauthenticated)
		}
	default:
		return Identity{}, fmt.Errorf("%w: unsupported token_use %q", ErrUnauthenticated, claims.TokenUse)
	}

	sub := strings.TrimSpace(claims.Subject)
	if sub == "" {
		return Identity{}, fmt.Errorf("%w: token has no subject", ErrUnauthenticated)
	}

	return Identity{UserID: sub, TenantID: sub}, nil
}

func audienceContains(aud jwt.ClaimStrings, want string) bool {
	for _, a := range aud {
		if a == want {
			return true
		}
	}
	return false
}
