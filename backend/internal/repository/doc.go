// Package repository is the storage boundary: the Store interface over the
// DynamoDB table and the Objects interface over the S3 bucket, with the
// conditional writes (row version, object ETag) that the concurrent-writer
// rules in docs/design/append-vs-autosave.md rest on. Every item lives under
// the tenant's partition and every object under its prefix (README,
// "Tenancy").
package repository
