package repository

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// deviceItemAttrs is a device row: every field a named attribute and no
// blob, since nothing lists devices by projection and the inbox rewrites the
// counter on every request.
func deviceItemAttrs(tenantID string, d model.Device) map[string]types.AttributeValue {
	item := map[string]types.AttributeValue{
		"pk":                strAttr(userPK(tenantID)),
		"sk":                strAttr(deviceSK(d.ID)),
		"type":              strAttr("device"),
		"device_id":         strAttr(d.ID),
		"name":              strAttr(d.Name),
		"key_hash":          strAttr(d.KeyHash),
		"created_at":        strAttr(d.CreatedAt),
		"last_used_at":      strAttr(d.LastUsedAt),
		"last_used_from":    strAttr(d.LastUsedFrom),
		"expires_at":        strAttr(d.ExpiresAt),
		"requests_day":      numAttr(d.RequestsDay),
		"requests_day_date": strAttr(d.RequestsDayDate),
		"requests_month":    numAttr(d.RequestsMonth),
		"bytes_month":       numAttr(d.BytesMonth),
		"month":             strAttr(d.Month),
		"revoked_at":        strAttr(d.RevokedAt),
		"version":           numAttr(d.Version),
	}
	if !d.Revoked() {
		item["gsi1pk"] = strAttr(deviceKeyGSI1PK(d.ID))
		item["gsi1sk"] = strAttr(deviceKeyGSI1SK)
	} else if at, err := model.ParseTime(d.RevokedAt); err == nil {
		item["ttl"] = numAttr(at.Add(model.RevokedDeviceRetention).Unix())
	}
	return item
}

func deviceFromItem(tenantID string, m map[string]types.AttributeValue) model.Device {
	return model.Device{
		ID:              readString(m, "device_id"),
		TenantID:        tenantID,
		Name:            readString(m, "name"),
		KeyHash:         readString(m, "key_hash"),
		CreatedAt:       readString(m, "created_at"),
		LastUsedAt:      readString(m, "last_used_at"),
		LastUsedFrom:    readString(m, "last_used_from"),
		ExpiresAt:       readString(m, "expires_at"),
		RequestsDay:     readInt(m, "requests_day"),
		RequestsDayDate: readString(m, "requests_day_date"),
		RequestsMonth:   readInt(m, "requests_month"),
		BytesMonth:      readInt(m, "bytes_month"),
		Month:           readString(m, "month"),
		RevokedAt:       readString(m, "revoked_at"),
		Version:         readInt(m, "version"),
	}
}

func (s *DynamoStore) PutDevice(ctx context.Context, tenantID string, d model.Device) (model.Device, error) {
	if err := ctx.Err(); err != nil {
		return model.Device{}, err
	}
	expected := d.Version
	next := d
	next.TenantID = tenantID
	next.Version = expected + 1
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(s.tableName),
		Item:                      deviceItemAttrs(tenantID, next),
		ConditionExpression:       aws.String(versionCondition(expected)),
		ExpressionAttributeValues: map[string]types.AttributeValue{":expected": numAttr(expected)},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return model.Device{}, ErrVersionConflict
		}
		return model.Device{}, fmt.Errorf("dynamo put device: %w", err)
	}
	return next, nil
}

func (s *DynamoStore) GetDevice(ctx context.Context, tenantID, deviceID string) (model.Device, error) {
	if err := ctx.Err(); err != nil {
		return model.Device{}, err
	}
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(deviceSK(deviceID)),
		},
		// Strongly consistent for the reason GetCapture is: a revoke is
		// PutDevice under the version this read returned.
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return model.Device{}, fmt.Errorf("dynamo get device: %w", err)
	}
	if out.Item == nil {
		return model.Device{}, ErrNotFound
	}
	return deviceFromItem(tenantID, out.Item), nil
}

func (s *DynamoStore) ListDevices(ctx context.Context, tenantID string) ([]model.Device, error) {
	var start map[string]types.AttributeValue
	out := []model.Device{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":        strAttr(userPK(tenantID)),
				":sk_prefix": strAttr("DEVICE#"),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamo query devices: %w", err)
		}
		for _, raw := range res.Items {
			out = append(out, deviceFromItem(tenantID, raw))
		}
		start = res.LastEvaluatedKey
		if len(start) == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// LookupDeviceKey is one index read for the row's keys and one GetItem for
// the row: the index projects captures' attributes, not a device's, and a
// device row is small enough that the second read costs less than widening
// the projection would (an index rebuild). The row comes back as it is,
// revoked or not: a revoke drops the index keys, but the index can carry
// the entry for a moment longer, and the caller's revoked_at check is what
// tells that case apart from a key never issued.
func (s *DynamoStore) LookupDeviceKey(ctx context.Context, keyID string) (model.Device, error) {
	if err := ctx.Err(); err != nil {
		return model.Device{}, err
	}
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.indexName),
		KeyConditionExpression: aws.String("gsi1pk = :pk AND begins_with(gsi1sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        strAttr(deviceKeyGSI1PK(keyID)),
			":sk_prefix": strAttr(deviceKeyGSI1SK),
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return model.Device{}, fmt.Errorf("dynamo query device key: %w", err)
	}
	if len(out.Items) == 0 {
		return model.Device{}, ErrNotFound
	}
	raw := out.Items[0]
	tenantID := trimPrefix(readString(raw, "pk"), "USER#")
	return s.GetDevice(ctx, tenantID, trimPrefix(readString(raw, "sk"), "DEVICE#"))
}
