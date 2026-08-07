package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

var lambdaAdapter *httpadapter.HandlerAdapter

func init() {
	ctx := context.Background()

	tableName := mustEnv("TABLE_NAME")
	contentBucket := mustEnv("CONTENT_BUCKET")
	allowedOrigin := mustEnv("ALLOWED_ORIGIN")

	llmBaseURL := envOr("LLM_BASE_URL", "https://api.minimax.io/v1")
	llmModel := envOr("LLM_MODEL", "MiniMax-M3")
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

	ssmClient := ssm.NewFromConfig(cfg)
	groqAPIKey, err := resolveSecret(ctx, ssmClient, "GROQ_API_KEY", "GROQ_API_KEY_PATH")
	if err != nil {
		log.Fatalf("Failed to resolve Groq API key: %v", err)
	}
	llmAPIKey, err := resolveSecret(ctx, ssmClient, "LLM_API_KEY", "LLM_API_KEY_PATH")
	if err != nil {
		log.Fatalf("Failed to resolve LLM API key: %v", err)
	}

	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	store := repository.NewDynamoStore(dynamoClient, tableName)
	objects := repository.NewS3Objects(s3Client, contentBucket)

	stt, err := provider.NewGroqSTT(groqAPIKey, "", "", nil)
	if err != nil {
		log.Fatalf("Failed to create Groq STT: %v", err)
	}

	llm, err := provider.NewOpenAICleanup(llmAPIKey, llmBaseURL, llmModel, nil)
	if err != nil {
		log.Fatalf("Failed to create OpenAI Cleanup: %v", err)
	}

	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)
	captureService := service.NewCaptureService(store, objects, stt, llm)

	router := handler.NewRouter(notesService, settingsService, captureService, allowedOrigin)
	lambdaAdapter = httpadapter.New(router)
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

// resolveSecret prefers a direct env value (local/dev), else fetches SecureString from SSM path env.
func resolveSecret(ctx context.Context, client *ssm.Client, valueEnv, pathEnv string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(valueEnv)); v != "" {
		return v, nil
	}
	path := strings.TrimSpace(os.Getenv(pathEnv))
	if path == "" {
		return "", fmt.Errorf("%s or %s is required", valueEnv, pathEnv)
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(path),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("ssm get %s: %w", path, err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil || strings.TrimSpace(*out.Parameter.Value) == "" {
		return "", fmt.Errorf("ssm parameter %s is empty", path)
	}
	return *out.Parameter.Value, nil
}

func Handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	return lambdaAdapter.ProxyWithContext(ctx, req)
}

func main() {
	lambda.Start(Handler)
}