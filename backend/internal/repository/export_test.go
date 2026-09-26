package repository

// NewS3ObjectsWithAPI builds the adapter over any client that speaks the calls
// it makes — the in-memory bucket in s3fake_test.go. It is compiled into the
// tests only: production wiring goes through NewS3Objects, whose concrete
// client is what the presigner needs, so an adapter built here cannot presign.
func NewS3ObjectsWithAPI(client S3API, bucket string) *S3Objects {
	return &S3Objects{client: client, bucket: bucket}
}
