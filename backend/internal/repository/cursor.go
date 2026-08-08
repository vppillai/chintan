package repository

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// A cursor is the DynamoDB LastEvaluatedKey, base64url-encoded. Every key
// attribute in this table is a string, so the wire form is a flat map.
//
// The cursor carries the partition it came from and decoding checks it against
// the partition being queried. A cursor is therefore not a way to read another
// tenant's page: handing tenant A's cursor to a query for tenant B is rejected
// rather than honoured.

func encodeCursor(key map[string]types.AttributeValue) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	flat := make(map[string]string, len(key))
	for k, v := range key {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("cursor: key attribute %q is not a string", k)
		}
		flat[k] = s.Value
	}
	raw, err := json.Marshal(flat)
	if err != nil {
		return "", fmt.Errorf("cursor: encode: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeCursor turns an opaque cursor back into an exclusive start key. wantPK
// is the partition the caller is querying; a cursor from a different partition
// is rejected.
func decodeCursor(cursor, pkAttr, wantPK string) (map[string]types.AttributeValue, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("cursor: not valid base64: %w", err)
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("cursor: not a valid key: %w", err)
	}
	if len(flat) == 0 || len(flat) > 4 {
		return nil, fmt.Errorf("cursor: unexpected key shape")
	}
	if got, ok := flat[pkAttr]; !ok || got != wantPK {
		return nil, fmt.Errorf("cursor: does not belong to this partition")
	}
	out := make(map[string]types.AttributeValue, len(flat))
	for k, v := range flat {
		out[k] = &types.AttributeValueMemberS{Value: v}
	}
	return out, nil
}
