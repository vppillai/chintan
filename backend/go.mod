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
// Every CI and deploy job resolves its Go from the two lines below via
// setup-go's go-version-file, so this is the only place the version is written
// down. The toolchain line pins the patch: without it `go 1.26` meant whatever
// the machine had — CI got 1.26.8 from the runner's tool cache while a laptop
// on 1.26.0 built against nineteen stdlib advisories 1.26.8 fixes, so
// govulncheck's answer depended on the machine. setup-go reads the toolchain
// directive in preference to the go line, and an older local Go downloads
// exactly this one under GOTOOLCHAIN=auto (the default). Bump it to the newest
// patch when Dependabot or a security notice says to.
go 1.26

toolchain go1.26.8

require (
	github.com/SherClockHolmes/webpush-go v1.4.0
	github.com/aws/aws-lambda-go v1.55.1
	github.com/aws/aws-sdk-go-v2 v1.47.1
	github.com/aws/aws-sdk-go-v2/config v1.33.6
	github.com/aws/aws-sdk-go-v2/credentials v1.20.6
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.21.7
	github.com/aws/aws-sdk-go-v2/service/budgets v1.52.1
	github.com/aws/aws-sdk-go-v2/service/cloudformation v1.81.1
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.69.1
	github.com/aws/aws-sdk-go-v2/service/lambda v1.110.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.113.4
	github.com/aws/aws-sdk-go-v2/service/ssm v1.78.1
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.1
	github.com/aws/smithy-go v1.28.2
	github.com/awslabs/aws-lambda-go-api-proxy v0.16.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.43.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.1 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/net v0.55.0 // indirect
)
