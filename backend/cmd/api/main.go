package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vppillai/chintan/backend/internal/auth"
	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/pipeline"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/upload"
)

var lambdaAdapter *httpadapter.HandlerAdapterV2

func init() {
	// Structured logging is installed before anything can log, so no startup
	// line escapes as unstructured text. cmd/worker does the same.
	obs.Setup(logLevel())

	ctx := context.Background()

	tableName := mustEnv("TABLE_NAME")
	contentBucket := mustEnv("CONTENT_BUCKET")
	allowedOrigin := mustEnv("ALLOWED_ORIGIN")
	// A wildcard origin alongside Allow-Credentials defeats the same-origin
	// policy. Refuse to start rather than serve it.
	if allowedOrigin == "*" {
		log.Fatalf("ALLOWED_ORIGIN must be a concrete origin, not %q", allowedOrigin)
	}

	awsRegion := os.Getenv("AWS_REGION")

	var cfg aws.Config
	var err error
	if awsRegion != "" {
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(awsRegion))
	} else {
		cfg, err = config.LoadDefaultConfig(ctx)
	}
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Token verification is not optional. The API Gateway authorizer is not
	// guaranteed to be the only ingress, so the service verifies for itself and
	// refuses to start unconfigured — auth may not silently degrade.
	clientID := mustEnv("USER_POOL_CLIENT_ID")
	issuer := strings.TrimSpace(os.Getenv("COGNITO_ISSUER"))
	if issuer == "" {
		issuer = auth.NewCognitoIssuer(awsRegion, os.Getenv("USER_POOL_ID"))
	}
	verifier, err := auth.NewCognitoVerifier(issuer, clientID, nil)
	if err != nil {
		log.Fatalf("Failed to build token verifier (set COGNITO_ISSUER or USER_POOL_ID, and USER_POOL_CLIENT_ID): %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	store := repository.NewDynamoStore(dynamoClient, tableName)
	objects := repository.NewS3Objects(s3Client, contentBucket)

	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)

	// Three wirings, each of which is the difference between a feature working
	// and a feature that only looks like it works:
	//
	//  - WithUploads installs the tag-aware presigner. Without it the fallback
	//    signs untagged PUTs, the lifecycle rule never matches the object, and
	//    RetentionDays goes back to being a setting that is stored, returned,
	//    rendered in the UI, and read by nothing.
	//  - WithQueue hands captures to the worker. Without it a retry has nowhere
	//    to go.
	//  - WithNoteCreator lets a user resolve a needs_target capture by naming a
	//    new note.
	captureService := service.NewCaptureService(store, objects).
		WithUploads(upload.NewS3(s3Client, contentBucket)).
		WithQueue(pipeline.NewQueue(sqs.NewFromConfig(cfg), mustEnv("CAPTURE_QUEUE_URL"))).
		WithNoteCreator(notesService)

	// The API does not call a provider, so it cannot spend. It reads the same
	// atomic counter the breaker enforces against, so a capped tenant is told
	// before it uploads rather than after the capture stalls.
	spendGate := service.NewSpendGate(
		pipeline.NewDynamoCounter(dynamoClient, tableName),
		settingsService,
		envInt64("DAILY_SPEND_CAP_MICROS", 0),
	)

	// Biometric unlock needs somewhere safe to keep one Cognito refresh token.
	// That used to be a customer-managed KMS key at $1/month, which on an
	// instance whose entire idle bill was that key is the whole bill. It is now
	// a 32-byte key in an SSM SecureString — encrypted under the free
	// AWS-managed aws/ssm key — and the token is sealed with AES-256-GCM.
	//
	// Fail closed, exactly as before. No key, no biometric unlock. There is no
	// plaintext path: the identity SealBox lives in a _test.go file and is not
	// compiled into this binary, so no configuration can select it.
	var webauthnService handler.WebAuthnAPI
	rpName := envOr("WEBAUTHN_RP_DISPLAY_NAME", "Chintan")
	if box, label, err := loadVaultBox(ctx, ssm.NewFromConfig(cfg)); err != nil {
		obs.Log(ctx).Warn("webauthn disabled", slog.String("reason", err.Error()))
	} else {
		refresher := &service.CognitoRefresher{
			Client:   cognitoidentityprovider.NewFromConfig(cfg),
			ClientID: clientID,
		}
		svc, err := service.NewWebAuthnService(store, allowedOrigin, rpName, refresher, box, verifier)
		if err != nil {
			obs.Log(ctx).Warn("webauthn disabled", slog.String("error", err.Error()))
		} else {
			webauthnService = svc
			obs.Log(ctx).Info("biometric unlock enabled", slog.String("vault_key", label))
		}
	}

	router := handler.New(handler.Deps{
		Notes:                 notesService,
		Settings:              settingsService,
		Captures:              captureService,
		Search:                service.NewSearchService(notesService),
		Tags:                  service.NewTagsService(notesService),
		Export:                service.NewExportService(notesService, captureService, settingsService, objects),
		Readiness:             service.NewReadinessService(store, objects),
		WebAuthn:              webauthnService,
		Spend:                 spendGate,
		Store:                 store,
		Verifier:              verifier,
		AllowedOrigin:         allowedOrigin,
		DefaultSpendCapMicros: envInt64("DAILY_SPEND_CAP_MICROS", 0),
	})
	lambdaAdapter = httpadapter.NewV2(router)
}

func logLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func mustEnv(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		log.Fatalf("%s environment variable is required", key)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// envInt64 refuses to start on a malformed value. A spend cap that silently
// reads as zero because somebody typed "10 USD" is a cap that does not exist.
func envInt64(key string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("%s must be a whole number of microdollars: %v", key, err)
	}
	return v
}

func Handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return lambdaAdapter.ProxyWithContext(ctx, req)
}

func main() {
	lambda.Start(Handler)
}

// loadVaultBox reads the refresh-token vault key from SSM and builds the box
// that seals with it.
//
// The key is a SecureString because CloudFormation cannot create one — the same
// reason the provider API keys are created out of band — so it is generated by
// scripts/bootstrap.sh and documented beside them.
//
// Every failure disables biometric unlock rather than degrading it. A missing
// parameter, an unreadable one, a value that is not 32 bytes: all of them mean
// there is nowhere safe to put a refresh token, and the honest response is to
// switch the feature off and say why in the log.
func loadVaultBox(ctx context.Context, client *ssm.Client) (*service.AESVaultBox, string, error) {
	path := strings.TrimSpace(os.Getenv("TOKEN_VAULT_KEY_PATH"))
	if path == "" {
		return nil, "", fmt.Errorf("TOKEN_VAULT_KEY_PATH is not set")
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(path),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return nil, "", fmt.Errorf("read vault key %s: %w", path, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return nil, "", fmt.Errorf("vault key %s is empty", path)
	}

	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*out.Parameter.Value))
	if err != nil {
		return nil, "", fmt.Errorf("vault key %s is not base64: %w", path, err)
	}
	// The parameter VERSION is the key label, so a rotation — putting a new
	// value at the same path — produces blobs that name the version that sealed
	// them. Without it, rotating would make every existing vault entry
	// indistinguishable from a corrupt one.
	label := fmt.Sprintf("ssm:%d", out.Parameter.Version)
	box, err := service.NewAESVaultBox(key, label)
	if err != nil {
		return nil, "", fmt.Errorf("vault key %s: %w", path, err)
	}
	return box, label, nil
}
