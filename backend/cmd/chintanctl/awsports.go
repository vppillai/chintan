package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamotypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// dynamoPartition implements Partition against a real table.
type dynamoPartition struct {
	client *dynamodb.Client
	table  string
}

func (d *dynamoPartition) Scan(ctx context.Context, pk, skPrefix string, fn func(Item) error) error {
	cond := "pk = :pk"
	values := map[string]dynamotypes.AttributeValue{
		":pk": &dynamotypes.AttributeValueMemberS{Value: pk},
	}
	if skPrefix != "" {
		cond += " AND begins_with(sk, :skp)"
		values[":skp"] = &dynamotypes.AttributeValueMemberS{Value: skPrefix}
	}

	var start map[string]dynamotypes.AttributeValue
	for {
		out, err := d.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(d.table),
			KeyConditionExpression:    aws.String(cond),
			ExpressionAttributeValues: values,
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return fmt.Errorf("query %s partition %s: %w", d.table, pk, err)
		}
		for _, raw := range out.Items {
			if err := fn(itemFromSDK(raw)); err != nil {
				return err
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		start = out.LastEvaluatedKey
	}
}

func (d *dynamoPartition) Put(ctx context.Context, it Item) error {
	_, err := d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.table),
		Item:      itemToSDK(it),
	})
	if err != nil {
		return fmt.Errorf("put item %s/%s: %w", it.PK(), it.SK(), err)
	}
	return nil
}

func (d *dynamoPartition) Delete(ctx context.Context, pk, sk string) error {
	_, err := d.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.table),
		Key: map[string]dynamotypes.AttributeValue{
			"pk": &dynamotypes.AttributeValueMemberS{Value: pk},
			"sk": &dynamotypes.AttributeValueMemberS{Value: sk},
		},
	})
	if err != nil {
		return fmt.Errorf("delete item %s/%s: %w", pk, sk, err)
	}
	return nil
}

func itemFromSDK(raw map[string]dynamotypes.AttributeValue) Item {
	out := make(Item, len(raw))
	for name, v := range raw {
		out[name] = attrFromSDK(v)
	}
	return out
}

func attrFromSDK(v dynamotypes.AttributeValue) AttrValue {
	switch t := v.(type) {
	case *dynamotypes.AttributeValueMemberS:
		s := t.Value
		return AttrValue{S: &s}
	case *dynamotypes.AttributeValueMemberN:
		n := t.Value
		return AttrValue{N: &n}
	case *dynamotypes.AttributeValueMemberB:
		return AttrValue{B: t.Value}
	case *dynamotypes.AttributeValueMemberBOOL:
		b := t.Value
		return AttrValue{BOOL: &b}
	case *dynamotypes.AttributeValueMemberNULL:
		n := t.Value
		return AttrValue{NULL: &n}
	case *dynamotypes.AttributeValueMemberSS:
		return AttrValue{SS: t.Value}
	case *dynamotypes.AttributeValueMemberNS:
		return AttrValue{NS: t.Value}
	case *dynamotypes.AttributeValueMemberBS:
		return AttrValue{BS: t.Value}
	case *dynamotypes.AttributeValueMemberL:
		list := make([]AttrValue, 0, len(t.Value))
		for _, e := range t.Value {
			list = append(list, attrFromSDK(e))
		}
		return AttrValue{L: list}
	case *dynamotypes.AttributeValueMemberM:
		m := make(map[string]AttrValue, len(t.Value))
		for k, e := range t.Value {
			m[k] = attrFromSDK(e)
		}
		return AttrValue{M: m}
	default:
		return AttrValue{}
	}
}

func itemToSDK(it Item) map[string]dynamotypes.AttributeValue {
	out := make(map[string]dynamotypes.AttributeValue, len(it))
	for name, v := range it {
		out[name] = attrToSDK(v)
	}
	return out
}

func attrToSDK(a AttrValue) dynamotypes.AttributeValue {
	switch {
	case a.S != nil:
		return &dynamotypes.AttributeValueMemberS{Value: *a.S}
	case a.N != nil:
		return &dynamotypes.AttributeValueMemberN{Value: *a.N}
	case a.BOOL != nil:
		return &dynamotypes.AttributeValueMemberBOOL{Value: *a.BOOL}
	case a.NULL != nil:
		return &dynamotypes.AttributeValueMemberNULL{Value: *a.NULL}
	case a.B != nil:
		return &dynamotypes.AttributeValueMemberB{Value: a.B}
	case a.SS != nil:
		return &dynamotypes.AttributeValueMemberSS{Value: a.SS}
	case a.NS != nil:
		return &dynamotypes.AttributeValueMemberNS{Value: a.NS}
	case a.BS != nil:
		return &dynamotypes.AttributeValueMemberBS{Value: a.BS}
	case a.L != nil:
		list := make([]dynamotypes.AttributeValue, 0, len(a.L))
		for _, e := range a.L {
			list = append(list, attrToSDK(e))
		}
		return &dynamotypes.AttributeValueMemberL{Value: list}
	case a.M != nil:
		m := make(map[string]dynamotypes.AttributeValue, len(a.M))
		for k, e := range a.M {
			m[k] = attrToSDK(e)
		}
		return &dynamotypes.AttributeValueMemberM{Value: m}
	default:
		return &dynamotypes.AttributeValueMemberNULL{Value: true}
	}
}

// s3Blobs implements Blobs against a real bucket.
type s3Blobs struct {
	client *s3.Client
	bucket string
}

func (b *s3Blobs) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list s3://%s/%s: %w", b.bucket, prefix, err)
		}
		for _, obj := range page.Contents {
			info := ObjectInfo{
				Key:  aws.ToString(obj.Key),
				Size: aws.ToInt64(obj.Size),
				ETag: strings.Trim(aws.ToString(obj.ETag), `"`),
			}
			if strings.HasSuffix(info.Key, "/") && info.Size == 0 {
				continue // a console-created folder marker, not content
			}
			if err := fn(info); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *s3Blobs) Prefixes(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list prefixes s3://%s/%s: %w", b.bucket, prefix, err)
		}
		for _, cp := range page.CommonPrefixes {
			out = append(out, aws.ToString(cp.Prefix))
		}
	}
	return out, nil
}

func (b *s3Blobs) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var missing *s3types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, errObjectMissing
		}
		return nil, fmt.Errorf("get s3://%s/%s: %w", b.bucket, key, err)
	}
	return out.Body, nil
}

func (b *s3Blobs) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if _, err := b.client.PutObject(ctx, in); err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", b.bucket, key, err)
	}
	return nil
}

func (b *s3Blobs) Delete(ctx context.Context, key string) error {
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete s3://%s/%s: %w", b.bucket, key, err)
	}
	return nil
}

// errObjectMissing is returned by Open for a key that is not there. reconcile
// treats it as a finding rather than a failure.
var errObjectMissing = errors.New("chintanctl: object not found")

// target names the concrete table and bucket a command acts on.
type target struct {
	Instance    string `json:"instance"`
	Environment string `json:"environment"`
	Region      string `json:"region,omitempty"`
	Table       string `json:"table"`
	Bucket      string `json:"bucket"`
}

// resolveTarget derives the physical resource names for an instance.
//
// The names come from infrastructure/template.yaml, which builds them as
// chintan-<instance>-<environment> for the table and
// chintan-content-<instance>-<accountId> for the bucket. The account is read
// from STS rather than asked for, and either name can be overridden when
// chintanctl is pointed somewhere the naming convention does not reach — a
// restore into a scratch bucket, for instance.
func resolveTarget(ctx context.Context, g globalFlags) (target, *dynamodb.Client, *s3.Client, error) {
	t := target{
		Instance:    g.instance,
		Environment: g.environment,
		Region:      g.region,
		Table:       g.table,
		Bucket:      g.bucket,
	}

	var opts []func(*config.LoadOptions) error
	if g.region != "" {
		opts = append(opts, config.WithRegion(g.region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return t, nil, nil, fmt.Errorf("load aws config: %w", err)
	}
	t.Region = cfg.Region

	if t.Table == "" {
		t.Table = fmt.Sprintf("chintan-%s-%s", g.instance, g.environment)
	}
	if t.Bucket == "" {
		account := g.account
		if account == "" {
			id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			if err != nil {
				return t, nil, nil, fmt.Errorf("resolve account id (pass --bucket or --account to skip): %w", err)
			}
			account = aws.ToString(id.Account)
		}
		t.Bucket = fmt.Sprintf("chintan-content-%s-%s", g.instance, account)
	}

	return t, dynamodb.NewFromConfig(cfg), s3.NewFromConfig(cfg), nil
}
