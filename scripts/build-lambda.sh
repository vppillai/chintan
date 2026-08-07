#!/bin/bash

# Build Lambda function for ARM64 architecture
set -e

cd "$(dirname "$0")/../backend"

echo "Building Lambda function..."
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -o bootstrap ./cmd/api

echo "Creating deployment zip..."
zip lambda-function.zip bootstrap

echo "Lambda function built and packaged successfully as lambda-function.zip"