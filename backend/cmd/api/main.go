package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"github.com/vppillai/chintan/backend/internal/handler"
	"github.com/vppillai/chintan/backend/internal/provider"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/service"
)

var lambdaAdapter *httpadapter.HandlerAdapter

func init() {
	// Read required environment variables
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		log.Fatal("TABLE_NAME environment variable is required")
	}

	contentBucket := os.Getenv("CONTENT_BUCKET")
	if contentBucket == "" {
		log.Fatal("CONTENT_BUCKET environment variable is required")
	}

	allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
	if allowedOrigin == "" {
		log.Fatal("ALLOWED_ORIGIN environment variable is required")
	}

	groqAPIKey := os.Getenv("GROQ_API_KEY")
	if groqAPIKey == "" {
		log.Fatal("GROQ_API_KEY environment variable is required")
	}

	llmAPIKey := os.Getenv("LLM_API_KEY")
	if llmAPIKey == "" {
		log.Fatal("LLM_API_KEY environment variable is required")
	}

	// Optional environment variables with defaults
	llmBaseURL := os.Getenv("LLM_BASE_URL")
	if llmBaseURL == "" {
		llmBaseURL = "https://api.minimax.io/v1"
	}

	llmModel := os.Getenv("LLM_MODEL")
	if llmModel == "" {
		llmModel = "MiniMax-M3"
	}

	// AWS region (optional, uses default if not set)
	awsRegion := os.Getenv("AWS_REGION")

	// Initialize AWS config
	var cfg aws.Config
	var err error
	if awsRegion != "" {
		cfg, err = config.LoadDefaultConfig(context.TODO(), config.WithRegion(awsRegion))
	} else {
		cfg, err = config.LoadDefaultConfig(context.TODO())
	}
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Initialize AWS clients
	dynamoClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)

	// Initialize repositories
	store := repository.NewDynamoStore(dynamoClient, tableName)
	objects := repository.NewS3Objects(s3Client, contentBucket)

	// Initialize providers
	stt, err := provider.NewGroqSTT(groqAPIKey, "", "", nil)
	if err != nil {
		log.Fatalf("Failed to create Groq STT: %v", err)
	}

	llm, err := provider.NewOpenAICleanup(llmAPIKey, llmBaseURL, llmModel, nil)
	if err != nil {
		log.Fatalf("Failed to create OpenAI Cleanup: %v", err)
	}

	// Initialize services
	notesService := service.NewNotesService(store, objects)
	settingsService := service.NewSettingsService(store)
	captureService := service.NewCaptureService(store, objects, stt, llm)

	// Create router
	router := handler.NewRouter(notesService, settingsService, captureService, allowedOrigin)

	// Initialize HTTP adapter for Lambda
	lambdaAdapter = httpadapter.New(router)
}

func Handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	return lambdaAdapter.ProxyWithContext(ctx, req)
}

func main() {
	lambda.Start(Handler)
}