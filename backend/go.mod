module github.com/vppillai/chintan/backend

// 1.25.13 is not a rounding-up of 1.25.0: it carries the fix for GO-2026-5856
// (CVE-2026-42505, a crypto/tls Encrypted Client Hello leak that govulncheck
// reaches through provider.GroqSTT.Transcribe -> http.Client.Do ->
// tls.Conn.HandshakeContext) and, as of this bump, two more: GO-2026-5972
// (encoding/asn1's recursion depth, reached through auth.CognitoVerifier.Verify
// parsing a JWT) and GO-2026-5026 (net/http accepting an ASCII-only
// Punycode-encoded label in golang.org/x/net/idna, reached the same way as the
// crypto/tls fix above). Lowering it re-opens all three.
//
// Every CI and deploy job resolves its Go from this line via setup-go's
// go-version-file, so this is the only place the version is written down.
go 1.26

require (
	github.com/aws/aws-lambda-go v1.55.0
	github.com/aws/aws-sdk-go-v2 v1.46.0
	github.com/aws/aws-sdk-go-v2/config v1.33.2
	github.com/aws/aws-sdk-go-v2/credentials v1.20.2
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.21.2
	github.com/aws/aws-sdk-go-v2/service/budgets v1.50.0
	github.com/aws/aws-sdk-go-v2/service/cloudformation v1.79.0
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.66.0
	github.com/aws/aws-sdk-go-v2/service/lambda v1.106.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.110.0
	github.com/aws/aws-sdk-go-v2/service/ssm v1.76.0
	github.com/aws/aws-sdk-go-v2/service/sts v1.48.0
	github.com/aws/smithy-go v1.28.1
	github.com/awslabs/aws-lambda-go-api-proxy v0.16.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.19.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.2 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.39.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.8.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.36.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.41.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)
