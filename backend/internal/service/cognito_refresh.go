package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cipTypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/vppillai/chintan/backend/internal/model"
)

// CognitoRefresher implements TokenRefresher via REFRESH_TOKEN_AUTH.
type CognitoRefresher struct {
	Client   *cognitoidentityprovider.Client
	ClientID string
}

func (c *CognitoRefresher) Refresh(ctx context.Context, refreshToken string) (model.CognitoTokenSet, error) {
	out, err := c.Client.InitiateAuth(ctx, &cognitoidentityprovider.InitiateAuthInput{
		AuthFlow: cipTypes.AuthFlowTypeRefreshTokenAuth,
		ClientId: aws.String(c.ClientID),
		AuthParameters: map[string]string{
			"REFRESH_TOKEN": refreshToken,
		},
	})
	if err != nil {
		return model.CognitoTokenSet{}, err
	}
	if out.AuthenticationResult == nil {
		return model.CognitoTokenSet{}, fmt.Errorf("cognito refresh returned no tokens")
	}
	ar := out.AuthenticationResult
	refresh := refreshToken
	if ar.RefreshToken != nil && *ar.RefreshToken != "" {
		refresh = *ar.RefreshToken
	}
	return model.CognitoTokenSet{
		IDToken:      aws.ToString(ar.IdToken),
		AccessToken:  aws.ToString(ar.AccessToken),
		RefreshToken: refresh,
		ExpiresIn:    ar.ExpiresIn,
		TokenType:    aws.ToString(ar.TokenType),
	}, nil
}

// KMSBox seals small secrets with KMS Encrypt/Decrypt.
type KMSBox struct {
	Client *kms.Client
	KeyID  string
}

func (b *KMSBox) Seal(ctx context.Context, plaintext []byte) ([]byte, error) {
	out, err := b.Client.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(b.KeyID),
		Plaintext: plaintext,
	})
	if err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

func (b *KMSBox) Open(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := b.Client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: ciphertext,
		KeyId:          aws.String(b.KeyID),
	})
	if err != nil {
		return nil, err
	}
	return out.Plaintext, nil
}

// PlainBox is an insecure test/dev seal (identity).
type PlainBox struct{}

func (PlainBox) Seal(ctx context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte(nil), plaintext...), nil
}
func (PlainBox) Open(ctx context.Context, ciphertext []byte) ([]byte, error) {
	return append([]byte(nil), ciphertext...), nil
}

// FakeRefresher returns a synthetic token set for tests.
type FakeRefresher struct {
	Sub          string
	RefreshToken string
	Err          error
}

func (f *FakeRefresher) Refresh(ctx context.Context, refreshToken string) (model.CognitoTokenSet, error) {
	if f.Err != nil {
		return model.CognitoTokenSet{}, f.Err
	}
	sub := f.Sub
	if sub == "" {
		sub = "user-1"
	}
	payload, _ := json.Marshal(map[string]string{"sub": sub})
	idTok := "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	rt := f.RefreshToken
	if rt == "" {
		rt = refreshToken
	}
	return model.CognitoTokenSet{
		IDToken: idTok, AccessToken: "access", RefreshToken: rt, ExpiresIn: 3600, TokenType: "Bearer",
	}, nil
}
