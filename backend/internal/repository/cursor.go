package repository

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// A cursor is the DynamoDB LastEvaluatedKey, base64url-encoded, plus the
// direction the query that minted it was walking. Every key attribute in this
// table is a string, so the wire form is a flat map.
//
// The cursor carries the partition it came from and decoding checks it against
// the partition being queried. A cursor is therefore not a way to read another
// tenant's page: handing tenant A's cursor to a query for tenant B is rejected
// rather than honoured.

// Cursor directions. A LastEvaluatedKey says where a walk stopped and nothing
// about which way it was going, and ExclusiveStartKey accepts it either way —
// so resuming an ascending cursor in a descending query is not an error, it is
// a walk back over ground already covered, which serves the caller a page of
// notes it has seen and never serves the ones it has not. Recording the
// direction is what turns that silent wrong answer into a refusal.
const (
	cursorAscending  = "asc"
	cursorDescending = "desc"
)

// cursorDirectionAttr is where the direction rides. It cannot collide with a
// key attribute: this table's keys are pk, sk, gsi1pk and gsi1sk.
const cursorDirectionAttr = "dir"

// cursorScope is everything decoding checks a cursor against.
type cursorScope struct {
	// pkAttr and wantPK are the partition being queried.
	pkAttr, wantPK string
	// skAttr and skPrefix reject a cursor from a different query over the same
	// partition — notes and captures share one partition, so a note cursor
	// would otherwise be accepted by a capture list and silently return the
	// wrong page.
	skAttr, skPrefix string
	// direction is the way this query walks its sort key.
	direction string
	// unmarked is the direction to assume for a cursor that records none,
	// i.e. one minted before cursors carried direction at all. It is the
	// direction THIS query used before that change, so a cursor in flight
	// across the deploy is honoured where the direction did not change and
	// refused where it did. Refusing is the point: the alternative for a note
	// list is a page the caller has already seen in place of the ones it has
	// not.
	unmarked string
}

func encodeCursor(key map[string]types.AttributeValue, direction string) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	flat := make(map[string]string, len(key)+1)
	for k, v := range key {
		s, ok := v.(*types.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("cursor: key attribute %q is not a string", k)
		}
		flat[k] = s.Value
	}
	flat[cursorDirectionAttr] = direction
	raw, err := json.Marshal(flat)
	if err != nil {
		return "", fmt.Errorf("cursor: encode: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeCursor turns an opaque cursor back into an exclusive start key.
func decodeCursor(cursor string, scope cursorScope) (map[string]types.AttributeValue, error) {
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

	direction := scope.unmarked
	if got, ok := flat[cursorDirectionAttr]; ok {
		direction = got
		delete(flat, cursorDirectionAttr)
	}
	if direction != scope.direction {
		// Loud, not silent. This is the one cursor fault that would otherwise
		// look like a working list.
		return nil, fmt.Errorf("cursor: was issued for a %s listing and this one is %s; start again from the first page",
			direction, scope.direction)
	}

	if len(flat) == 0 || len(flat) > 4 {
		return nil, fmt.Errorf("cursor: unexpected key shape")
	}
	if got, ok := flat[scope.pkAttr]; !ok || got != scope.wantPK {
		return nil, fmt.Errorf("cursor: does not belong to this partition")
	}
	if scope.skPrefix != "" {
		got, ok := flat[scope.skAttr]
		if !ok || !strings.HasPrefix(got, scope.skPrefix) {
			return nil, fmt.Errorf("cursor: does not belong to this query")
		}
	}
	out := make(map[string]types.AttributeValue, len(flat))
	for k, v := range flat {
		out[k] = &types.AttributeValueMemberS{Value: v}
	}
	return out, nil
}
