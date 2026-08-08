module github.com/vppillai/chintan/backend

// 1.25.12 is not a rounding-up of 1.25.0: it is the first 1.25 patch carrying
// the fix for GO-2026-5856 (CVE-2026-42505), a crypto/tls Encrypted Client
// Hello leak that govulncheck reaches through provider.GroqSTT.Transcribe ->
// http.Client.Do -> tls.Conn.HandshakeContext. Lowering it re-opens that.
//
// Every CI and deploy job resolves its Go from this line via setup-go's
// go-version-file, so this is the only place the version is written down.
go 1.25.12

require (
	github.com/aws/aws-lambda-go v1.54.0
	github.com/aws/aws-sdk-go-v2 v1.43.4
	github.com/aws/aws-sdk-go-v2/config v1.32.35
	github.com/aws/aws-sdk-go-v2/credentials v1.19.34
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.20.59
	github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider v1.67.4
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.63.1
	github.com/aws/aws-sdk-go-v2/service/kms v1.55.4
	github.com/aws/aws-sdk-go-v2/service/s3 v1.107.0
	github.com/aws/aws-sdk-go-v2/service/sqs v1.46.4
	github.com/aws/aws-sdk-go-v2/service/ssm v1.73.4
	github.com/awslabs/aws-lambda-go-api-proxy v0.16.2
	github.com/go-webauthn/webauthn v0.17.4
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.16 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.35 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.35 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.35 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.36 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.36.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.15 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.28 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.12.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.35 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.36 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.33.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.38.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.45.4 // indirect
	github.com/aws/smithy-go v1.27.6 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)
