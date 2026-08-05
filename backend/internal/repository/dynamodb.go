package repository

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/keys"
)

// Table attribute names, matching infrastructure/template.yaml. PK and SK are the
// composite primary key; ttl is the TimeToLiveSpecification attribute. The GSI1 names
// come from the keys package rather than being repeated here, because a mismatch
// between the name written and the name the index is defined on produces a record that
// is stored correctly and never appears in a listing.
const (
	pkAttr  = "PK"
	skAttr  = "SK"
	ttlAttr = "ttl"
)

// DynamoAPI is the DynamoDB surface this adapter uses.
//
// Four operations, satisfied by *dynamodb.Client. Narrow and locally declared so that
// tests need no AWS credentials and no AWS fake framework (§0.5A: the whole check suite
// runs in a job that holds no credentials) — and so that the absence of Scan is a
// property of the type rather than of the current implementation.
type DynamoAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// Dynamo is the DynamoDB-backed Repository — the production implementation.
//
// memory.go is its specification, not the other way round: every test in this project
// runs against the fake and only production runs against this type, so a behaviour the
// two do not share is a bug that cannot be observed until it is in front of a user.
// Where the two genuinely cannot agree the divergence is named in a comment at the point
// it happens rather than left for someone to discover from a support ticket, and this is
// the complete list of those places:
//
//   - validateKey refuses a key with an empty component, where the fake returns
//     ErrNotFound for such a Get and no-ops such a Delete (see validateKey).
//   - typed collections and structs come back as []any and map[string]any, because
//     DynamoDB stores no element type and no field type (see unmarshalValue and
//     marshalStruct).
//   - a whole float64 comes back as int64, because DynamoDB has one number type — which
//     is why AsInt64 and AsFloat64 exist (see unmarshalNumber).
//   - a ttl written into Attrs comes back in Item.TTL only (see unmarshalItem).
//
// Everything else is shared code rather than a matched pair. validateItem — the key
// check, the 400KB ceiling, and which values are representable at all — is one function
// that both implementations call, so "the two reject the same items" is structural
// instead of something to keep remembering. In particular the fake refuses a value this
// adapter cannot store faithfully, and it refuses it by asking this adapter's marshaller
// rather than by keeping a second copy of the rules.
//
// The surface deliberately offers no Scan and no index query. I11 requires that no read
// path exists that is not qualified by tenant_id, and the way that requirement dies is
// not a deliberate table-wide read — it is a "just for the admin script" Scan added
// eighteen months later. No method here could be adapted into one, and DynamoAPI does
// not include Scan, so adding one is a visible change to an interface rather than an
// extra line in a function body.
//
// This adapter writes no metering record (I12) and no audit record (I13), and both
// omissions are deliberate:
//
//   - Metering is itself a repository write (meter.Record calls PutOnce), so metering
//     every repository call would recurse without bound. §6.4 also assigns
//     shared-infrastructure cost — one table, one function — to the deployment tag set
//     rather than to per-tenant Usage, because "AWS cost allocation tags cannot
//     attribute shared-resource spend across tenants". Per-tenant storage is metered as
//     storage_bytes by the code that writes objects, which knows the byte count.
//   - Auditing has the same recursion, since audit records are stored through this
//     interface, and "access to user content" in I13 is a request-level event. An audit
//     trail with one row per internal Get is one nobody can read, which is the same as
//     not having one.
type Dynamo struct {
	api   DynamoAPI
	table string
}

// Compile-time proof that the adapter and the fake implement the same contract. Without
// it, a signature drift shows up as a nil-pointer panic at wiring time.
var _ Repository = (*Dynamo)(nil)

// NewDynamo builds the adapter.
//
// The table name is a parameter rather than read from CHINTAN_TABLE here: a package that
// reads its own environment cannot be tested against two tables, and the failure mode
// when the variable is missing is an empty table name reaching the SDK, which surfaces
// as a validation error from AWS rather than as a configuration error at startup. The
// caller reads the variable and gets that error here, at cold start, before any request.
func NewDynamo(api DynamoAPI, table string) (*Dynamo, error) {
	if api == nil {
		return nil, fmt.Errorf("repository: DynamoAPI is nil")
	}
	if table == "" {
		return nil, fmt.Errorf("repository: table name is empty; it comes from CHINTAN_TABLE, which the CloudFormation template sets on every function (§6.3)")
	}
	return &Dynamo{api: api, table: table}, nil
}

// Get returns one item, or ErrNotFound.
//
// Strongly consistent. DynamoDB's default is eventually consistent, and the fake is
// immediately consistent, so a default read would make read-after-write succeed in every
// test and fail occasionally in production — the pipeline writes a capture record and
// then reads it in a later stage. Consistent reads cost twice as much per read, which at
// the modelled volume of ~45 segments/day (§10.7) is a rounding error against a bug class
// that is invisible until it is a lost transcript.
func (d *Dynamo) Get(ctx context.Context, key keys.DynamoKey) (*Item, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	out, err := d.api.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.table),
		Key:            keyAttrs(key),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("repository: get %s / %s: %w", key.PK, key.SK, err)
	}
	if len(out.Item) == 0 {
		// A missing item is an empty map and a nil error from the SDK. Translating it
		// here is the whole reason ErrNotFound exists: a caller that forgot to check
		// for an empty map would otherwise proceed with a zero-valued record.
		return nil, fmt.Errorf("%w: %s / %s", ErrNotFound, key.PK, key.SK)
	}
	item, err := unmarshalItem(out.Item)
	if err != nil {
		return nil, fmt.Errorf("repository: get %s / %s: %w", key.PK, key.SK, err)
	}
	return &item, nil
}

// Put writes or replaces one item.
func (d *Dynamo) Put(ctx context.Context, item Item) error {
	av, err := marshalItem(item)
	if err != nil {
		return err
	}
	if _, err := d.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.table),
		Item:      av,
	}); err != nil {
		return fmt.Errorf("repository: put %s / %s: %w", item.Key.PK, item.Key.SK, err)
	}
	return nil
}

// PutOnce writes one item and fails with ErrAlreadyExists if the key is taken.
//
// The condition is attribute_not_exists on the partition key, which DynamoDB evaluates
// against the single item addressed by PK+SK — so it means "no item with this exact
// key", not "nothing in this partition". Getting that wrong in either direction is
// serious: a condition on a non-key attribute would let a second write through and
// create the duplicate capture that idempotency exists to prevent (§2A.1), and a
// condition that never passes would make every audit write fail.
func (d *Dynamo) PutOnce(ctx context.Context, item Item) error {
	av, err := marshalItem(item)
	if err != nil {
		return err
	}
	_, err = d.api.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                aws.String(d.table),
		Item:                     av,
		ConditionExpression:      aws.String("attribute_not_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{"#pk": pkAttr},
	})
	if err != nil {
		// errors.As rather than a string match on the message: the message is not part
		// of the API contract and has changed between SDK releases. Only this one error
		// becomes ErrAlreadyExists — a throttle or a timeout translated to
		// "already exists" would make an idempotent retry report success while nothing
		// was written, which is the failure that loses a capture silently.
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return fmt.Errorf("%w: %s / %s", ErrAlreadyExists, item.Key.PK, item.Key.SK)
		}
		return fmt.Errorf("repository: put-once %s / %s: %w", item.Key.PK, item.Key.SK, err)
	}
	return nil
}

// QueryPrefix returns every item in one partition whose sort key begins with the given
// prefix, in sort-key order. limit <= 0 means no limit, matching the fake.
//
// Paginates to exhaustion. DynamoDB returns at most 1MB per page and signals more with
// LastEvaluatedKey; a month of usage records exceeds that, and returning the first page
// would make MonthTotal under-count. The consequence is not a truncated list a caller
// would notice — it is a spend figure that looks plausible and is too low, so the daily
// cap in §10.5.9 stops holding. A wrong answer that looks right is worse than an error,
// so this returns no partial result on failure: the caller gets nil and an error, never
// the pages that happened to arrive before the one that failed.
func (d *Dynamo) QueryPrefix(ctx context.Context, pk, skPrefix string, limit int) ([]Item, error) {
	if pk == "" {
		// Refused rather than treated as "every partition". An empty partition key is
		// the only shape this method could have that would read across tenants (I11),
		// and it means the caller bypassed the keys package.
		return nil, fmt.Errorf("repository: query has an empty partition key; every query must be tenant-scoped and the partition key must come from the keys package (I11)")
	}

	cond := "#pk = :pk"
	names := map[string]string{"#pk": pkAttr}
	values := map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: pk}}
	if skPrefix != "" {
		// Only when non-empty: begins_with with an empty operand is a ValidationException,
		// while the fake treats an empty prefix as "match everything in the partition".
		// Omitting the clause is what makes the two agree.
		cond += " AND begins_with(#sk, :prefix)"
		names["#sk"] = skAttr
		values[":prefix"] = &types.AttributeValueMemberS{Value: skPrefix}
	}

	var out []Item
	var startKey map[string]types.AttributeValue
	for {
		in := &dynamodb.QueryInput{
			TableName:                 aws.String(d.table),
			KeyConditionExpression:    aws.String(cond),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			// Explicit rather than relying on the default, because sort-key order is
			// load-bearing: keys.Segment zero-pads the sequence precisely so that
			// lexicographic order reassembles the transcript correctly.
			ScanIndexForward: aws.Bool(true),
			// Consistent for the same reason as Get. Legal here because this queries the
			// base table; a GSI cannot be read consistently, which is one more reason
			// there is no index query on this interface.
			ConsistentRead:    aws.Bool(true),
			ExclusiveStartKey: startKey,
		}
		if limit > 0 {
			// Ask for only what is still missing, so a limited query does not pay to
			// read a whole partition. Clamped because Limit is an int32 and limit is an
			// int: a caller passing math.MaxInt would otherwise wrap to a negative
			// Limit, which DynamoDB rejects.
			remaining := limit - len(out)
			if remaining > math.MaxInt32 {
				remaining = math.MaxInt32
			}
			in.Limit = aws.Int32(int32(remaining))
		}

		page, err := d.api.Query(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("repository: query %s prefix %q: %w", pk, skPrefix, err)
		}
		for _, raw := range page.Items {
			item, err := unmarshalItem(raw)
			if err != nil {
				return nil, fmt.Errorf("repository: query %s prefix %q: %w", pk, skPrefix, err)
			}
			out = append(out, item)
		}
		if limit > 0 && len(out) >= limit {
			out = out[:limit]
			return out, nil
		}
		// An empty page with a LastEvaluatedKey is legitimate — DynamoDB stops at the
		// 1MB boundary, not at an item boundary — so the loop condition is the presence
		// of a continuation key and never "the page was empty". Stopping on an empty
		// page is the subtle version of the truncation bug this method exists to avoid.
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		startKey = page.LastEvaluatedKey
	}
}

// Delete removes one item. Absent is not an error, matching both DynamoDB and the fake.
//
// No guard against deleting a write-once record. That protection is deliberately
// structural rather than defensive: §6.3 and I13 are upheld by no code path calling
// Delete on an audit or usage key, and verify.sh (§11.6) asserting those records are
// still present. A guard here would need an audit-key literal, which check-tenant-keys.sh
// forbids outside the keys package, and would also block the tenant-erasure path (§9.3)
// that must be able to delete them — G-038's conflict, resolved the same way it is
// resolved everywhere else in this system.
func (d *Dynamo) Delete(ctx context.Context, key keys.DynamoKey) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if _, err := d.api.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(d.table),
		Key:       keyAttrs(key),
	}); err != nil {
		return fmt.Errorf("repository: delete %s / %s: %w", key.PK, key.SK, err)
	}
	return nil
}

func keyAttrs(key keys.DynamoKey) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		pkAttr: &types.AttributeValueMemberS{Value: key.PK},
		skAttr: &types.AttributeValueMemberS{Value: key.SK},
	}
}

// validateKey refuses a key with an empty component on the read and delete paths.
//
// **This diverges from the fake, which returns ErrNotFound for a Get with an empty key
// and silently no-ops such a Delete.** The divergence is chosen rather than accidental:
// an empty key component means the caller did not go through the keys package, and the
// fake's behaviour hides that, while DynamoDB would reject it anyway with an opaque
// validation error. Both implementations fail closed — no caller ever receives another
// tenant's data — so the divergence cannot leak; it can only change which error a test
// sees, and only for a key the keys package cannot produce.
func validateKey(key keys.DynamoKey) error {
	if key.PK == "" || key.SK == "" {
		return fmt.Errorf("repository: key has an empty component (PK=%q SK=%q); keys must come from the keys package (I11)", key.PK, key.SK)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Marshalling
// ---------------------------------------------------------------------------

// marshalItem converts an Item to a DynamoDB attribute map.
//
// Reuses validateItem from memory.go rather than reimplementing the checks, because
// "the two implementations reject the same items" is a property that only holds if there
// is one copy of the rule. That includes the 400KB ceiling: the fake enforces it to force
// the S3 overflow path for long verbatim prompt bodies (§3A.4), and this enforces it so
// that path is taken in production for the same inputs — the alternative is a
// ValidationException from AWS on the one item type §3A.3 says must not be truncated.
// measureAttrs is an approximation and DynamoDB's own accounting is stricter, so passing
// this check is not a guarantee the service accepts the item; the service's error still
// surfaces wrapped. What the check does guarantee is that no item the fake rejects is
// attempted here.
//
// Attribute values are therefore marshalled twice on this path — once to measure and
// check them, once below to build the request. At §10.7 volume (small records, ~45
// segments/day) that is not worth removing, and the alternative is a second traversal
// with a second copy of the value rules, which is the class of bug this arrangement
// exists to prevent.
func marshalItem(item Item) (map[string]types.AttributeValue, error) {
	if err := validateItem(item); err != nil {
		return nil, err
	}
	if item.TTL < 0 {
		return nil, fmt.Errorf("repository: TTL %d is negative", item.TTL)
	}
	// One set without the other produces a record DynamoDB stores happily and never
	// projects into GSI1, so the capture exists but never appears in the time-ordered
	// listing (§6.3). There is no error from AWS for this, which is why it is checked
	// here.
	if (item.GSI1PK == "") != (item.GSI1SK == "") {
		return nil, fmt.Errorf("repository: GSI1PK=%q and GSI1SK=%q — both or neither; one alone silently fails to project into GSI1 (§6.3)", item.GSI1PK, item.GSI1SK)
	}

	av := make(map[string]types.AttributeValue, len(item.Attrs)+5)
	for k, v := range item.Attrs {
		switch k {
		case pkAttr, skAttr, keys.GSI1PKAttr, keys.GSI1SKAttr:
			// Refused rather than silently overwritten from the struct fields. A caller
			// setting these in Attrs believes they will be honoured, and a key attribute
			// assembled by hand is exactly what check-tenant-keys.sh exists to catch.
			return nil, fmt.Errorf("repository: attribute %q is a key attribute; set it through Item.Key or Item.GSI1PK/GSI1SK, not Attrs (I11)", k)
		case ttlAttr:
			// Permitted, because §6.3's usage record carries ttl as a descriptive
			// attribute and meter writes it in both places. Item.TTL is authoritative
			// and a disagreement is refused: guessing which one the caller meant either
			// expires a record early or never expires it, and both are silent.
			n, ok := AsInt64(v)
			if !ok {
				return nil, fmt.Errorf("repository: attribute %q must be an integral epoch second, got %T", ttlAttr, v)
			}
			if n != item.TTL {
				return nil, fmt.Errorf("repository: Attrs[%q]=%d disagrees with Item.TTL=%d; Item.TTL is authoritative, so set both to the same value or only Item.TTL", ttlAttr, n, item.TTL)
			}
			continue
		}
		mv, err := marshalValue(v)
		if err != nil {
			return nil, fmt.Errorf("repository: attribute %q: %w", k, err)
		}
		av[k] = mv
	}

	av[pkAttr] = &types.AttributeValueMemberS{Value: item.Key.PK}
	av[skAttr] = &types.AttributeValueMemberS{Value: item.Key.SK}
	if item.GSI1PK != "" {
		av[keys.GSI1PKAttr] = &types.AttributeValueMemberS{Value: item.GSI1PK}
		av[keys.GSI1SKAttr] = &types.AttributeValueMemberS{Value: item.GSI1SK}
	}
	if item.TTL > 0 {
		// Only when positive. A ttl attribute of 0 is epoch 1970, which makes the item
		// immediately eligible for TTL deletion — so writing the zero value of the field
		// as an attribute would quietly delete every record that did not ask to expire.
		av[ttlAttr] = &types.AttributeValueMemberN{Value: strconv.FormatInt(item.TTL, 10)}
	}
	return av, nil
}

// marshalValue converts one attribute value.
//
// Integers are formatted with FormatInt, never through a float. DynamoDB numbers travel
// as decimal strings, so an int64 that went via float64 would arrive rounded: cost_micros
// of 9007199254740993 becomes ...992, and §Phase 0 requires reconciliation against the
// provider invoice to within 5% — a corpus of rounded costs is not reconcilable and the
// error is not recoverable from the stored record.
//
// **The write path is closed under the read path: every value accepted here reads back
// through unmarshalValue equal to what was written.** That property is not decoration.
// The first version of this code emitted integer decimal strings that unmarshalNumber
// then refused, and the refusal was raised for the whole item — so one such attribute
// made the record permanently undecodable and, through QueryPrefix, took the entire
// partition with it. TestNumberRoundTripClosureHolds asserts the property rather than
// trusting it, because the fake stores the exact Go value it was handed and so cannot
// observe an asymmetry here at all.
func marshalValue(v any) (types.AttributeValue, error) {
	return marshalValueAt(v, 0)
}

// maxNestingDepth is DynamoDB's documented ceiling on nested attribute levels.
//
// Enforced here for two reasons. Past the ceiling the service answers with a
// ValidationException a caller cannot tell from a throttle — the same reason the 400KB
// check exists rather than letting AWS refuse the item. And it terminates a structure that
// refers to itself: without a bound, `m["self"] = m` recurses until the stack is gone,
// which in the worker is a process death mid-capture, and I2 says audio is never lost to a
// software bug. A crash is the one failure mode a retry cannot report.
const maxNestingDepth = 32

func marshalValueAt(v any, depth int) (types.AttributeValue, error) {
	if depth > maxNestingDepth {
		return nil, fmt.Errorf("value nests deeper than DynamoDB's %d-level limit, or refers to itself", maxNestingDepth)
	}
	if v == nil {
		return &types.AttributeValueMemberNULL{Value: true}, nil
	}
	switch t := v.(type) {
	case string:
		return &types.AttributeValueMemberS{Value: t}, nil
	case bool:
		return &types.AttributeValueMemberBOOL{Value: t}, nil
	case []byte:
		return &types.AttributeValueMemberB{Value: t}, nil
	case int64:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(t, 10)}, nil
	case int:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(int64(t), 10)}, nil
	case float64:
		return marshalFloat(t)
	}

	// Reflection for the rest, so that []string, map[string]int, []model.Something and
	// the other shapes a caller naturally builds work without this switch having to
	// enumerate them. Enumerating them is how one gets missed and a field silently
	// fails to store.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return &types.AttributeValueMemberS{Value: rv.String()}, nil
	case reflect.Bool:
		return &types.AttributeValueMemberBOOL{Value: rv.Bool()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &types.AttributeValueMemberN{Value: strconv.FormatInt(rv.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if u := rv.Uint(); u > math.MaxInt64 {
			// Refused rather than written as a 20-digit decimal. DynamoDB would store it
			// happily and the read path would hand back 18446744073709551616 for a stored
			// 18446744073709551615 — a value that was never written, in a record no later
			// code can tell is wrong. Where the round trip cannot be exact the write fails
			// here, at the call site that still holds the value, rather than degrading a
			// stored record. Both implementations refuse it, because validateItem asks
			// this function (see measureAttrs).
			return nil, fmt.Errorf("value %d exceeds int64, so it cannot be read back exactly (DynamoDB numbers decode to int64 or float64); store the digits as a string if they matter", u)
		}
		return &types.AttributeValueMemberN{Value: strconv.FormatUint(rv.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		return marshalFloat(rv.Float())
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return &types.AttributeValueMemberNULL{Value: true}, nil
		}
		return marshalValueAt(rv.Elem().Interface(), depth+1)
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			return &types.AttributeValueMemberB{Value: rv.Bytes()}, nil
		}
		list := make([]types.AttributeValue, rv.Len())
		for i := range list {
			e, err := marshalValueAt(rv.Index(i).Interface(), depth+1)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			list[i] = e
		}
		return &types.AttributeValueMemberL{Value: list}, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key type %s is not a string; DynamoDB maps are string-keyed", rv.Type().Key())
		}
		m := make(map[string]types.AttributeValue, rv.Len())
		for _, k := range rv.MapKeys() {
			e, err := marshalValueAt(rv.MapIndex(k).Interface(), depth+1)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k.String(), err)
			}
			m[k.String()] = e
		}
		return &types.AttributeValueMemberM{Value: m}, nil
	case reflect.Struct:
		return marshalStruct(rv, depth)
	}
	return nil, fmt.Errorf("unsupported value of type %T; reduce it to a string, number, bool, []byte, slice, or map[string]any before storing", v)
}

// marshalStruct reduces a plain-data struct to a DynamoDB map.
//
// Accepted rather than refused because consent.decodeGrants documents
// map[string]model.ConsentGrant and map[Purpose]model.ConsentGrant as shapes the consent
// attribute legitimately arrives in, and the fake stores them without complaint — so
// refusing structs here meant a tenant provisioner writing the documented shape passed
// every test and failed on every production write with "unsupported value of type
// model.ConsentGrant". A latent trap of exactly the kind this package's two
// implementations exist to eliminate.
//
// Field names come from the `json` tag when the type declares one, so the mapping is the
// one already written on the type rather than a second, divergent copy of it; §6.3's
// attribute names are the json names, and model.ConsentGrant's tags are what make the
// stored map decode through consent.decodeGrant's map[string]any branch.
//
// A struct with any unexported field is still refused, loudly, because a reflected
// reduction would drop that field and a record that comes back missing something the
// writer believed it stored is not recoverable from the record. time.Time is the concrete
// case, and the right encoding for a timestamp here is clock.RFC3339UTC anyway.
//
// One behaviour worth stating because it differs from encoding/json: an embedded struct
// nests under its own field name rather than being flattened. Nothing stores one, and
// reimplementing json's flattening rules is the divergent second copy this reduction was
// written to avoid — so the note is here for the first caller who tries it.
func marshalStruct(rv reflect.Value, depth int) (types.AttributeValue, error) {
	rt := rv.Type()
	m := make(map[string]types.AttributeValue, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			return nil, fmt.Errorf("type %s has unexported field %q, which a reflected reduction would drop without saying so; build the map[string]any at the call site that knows the type", rt, f.Name)
		}
		name, store := jsonFieldName(f)
		if !store {
			continue
		}
		e, err := marshalValueAt(rv.Field(i).Interface(), depth+1)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		m[name] = e
	}
	return &types.AttributeValueMemberM{Value: m}, nil
}

// jsonFieldName reports the stored attribute name for one struct field, and whether it is
// stored at all. `json:"-"` is honoured; the option list after the name is not, because
// omitempty would make the stored attribute set depend on the value, and §6.3 gives every
// entity a fixed attribute set that a reader can rely on being present.
func jsonFieldName(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name, true
	}
	name, _, hasOpts := strings.Cut(tag, ",")
	switch {
	case name == "-" && !hasOpts:
		return "", false
	case name == "":
		return f.Name, true
	default:
		return name, true
	}
}

// measureAttrs marshals every attribute of an item and reports the approximate stored
// size. Called by validateItem, so both implementations share it.
//
// Marshalling rather than inspecting matters in two ways, and the previous estimate — a
// flat 8 bytes for a []byte, a list, or a map, whatever it contained — got both wrong:
//
//   - It measures what is actually stored. A 600KB body assembled as a list of blocks, or
//     as map[string]any{"text": ...}, or a 600KB []byte, all passed a check whose entire
//     purpose is to force the S3 overflow path (§3A.4) for the one content type §3A.3 says
//     must not be truncated. The write then failed at the service with a
//     ValidationException the caller cannot distinguish from a throttle, instead of an
//     error here that names text_key and the overflow path.
//   - It makes "both implementations reject the same items" structural rather than
//     remembered: the fake refuses a value the adapter cannot store faithfully because it
//     asks the adapter's marshaller, not because someone kept a second copy of the rules
//     in sync.
func measureAttrs(item Item) (int, error) {
	n := len(item.Key.PK) + len(item.Key.SK) + len(item.GSI1PK) + len(item.GSI1SK)
	for k, v := range item.Attrs {
		av, err := marshalValue(v)
		if err != nil {
			return 0, fmt.Errorf("repository: attribute %q: %w", k, err)
		}
		n += len(k) + encodedSize(av)
	}
	return n, nil
}

// encodedSize approximates the stored size of one attribute value.
//
// Approximate is sufficient — its job is to force the S3 overflow path (§3A.4), not to
// predict a bill — but it must not be blind, which is the difference between an estimate
// and a hole in the ceiling. DynamoDB counts the bytes of a binary attribute and of every
// element of a list or map, plus roughly one byte of overhead per element, so that is what
// this counts. It errs low rather than high, which keeps the service's accounting the
// stricter of the two: nothing is refused here that DynamoDB would have accepted, and an
// item that passes may still be refused by the service, whose error surfaces wrapped.
func encodedSize(av types.AttributeValue) int {
	switch t := av.(type) {
	case *types.AttributeValueMemberS:
		return len(t.Value)
	case *types.AttributeValueMemberN:
		return len(t.Value)
	case *types.AttributeValueMemberB:
		return len(t.Value)
	case *types.AttributeValueMemberBOOL, *types.AttributeValueMemberNULL:
		return 1
	case *types.AttributeValueMemberL:
		n := 0
		for _, e := range t.Value {
			n += 1 + encodedSize(e)
		}
		return n
	case *types.AttributeValueMemberM:
		n := 0
		for k, e := range t.Value {
			n += 1 + len(k) + encodedSize(e)
		}
		return n
	case *types.AttributeValueMemberSS:
		n := 0
		for _, s := range t.Value {
			n += 1 + len(s)
		}
		return n
	case *types.AttributeValueMemberNS:
		n := 0
		for _, s := range t.Value {
			n += 1 + len(s)
		}
		return n
	case *types.AttributeValueMemberBS:
		n := 0
		for _, b := range t.Value {
			n += 1 + len(b)
		}
		return n
	}
	// Unreachable: marshalValue produces only the types above. Counted as something rather
	// than as zero, because a future value shape that measured zero would be a silent
	// reopening of exactly the hole this function was written to close.
	return 8
}

func marshalFloat(f float64) (types.AttributeValue, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// DynamoDB has no representation for these, and the SDK reports it as an opaque
		// validation error from the service rather than as a bad input here.
		return nil, fmt.Errorf("value %v is not a finite number and cannot be stored as a DynamoDB number", f)
	}
	// 'f' with precision -1: the shortest decimal that round-trips exactly, and no
	// exponent form, so the stored value reads the same in the console and in the
	// exported JSON a cost reconciliation reads.
	return &types.AttributeValueMemberN{Value: strconv.FormatFloat(f, 'f', -1, 64)}, nil
}

// unmarshalItem converts a stored attribute map back to an Item.
//
// The five reserved attributes become struct fields and are removed from Attrs, so Attrs
// holds only what a caller put there. **This means the ttl a caller wrote into Attrs
// alongside Item.TTL does not survive the round trip in Attrs — it comes back in
// Item.TTL only.** The alternative, leaving ttl in Attrs, would add a key that callers
// who set only Item.TTL never wrote. Treating all five reserved names the same way is
// the version of this that nobody has to remember.
func unmarshalItem(av map[string]types.AttributeValue) (Item, error) {
	var item Item
	attrs := make(map[string]any, len(av))
	for k, v := range av {
		switch k {
		case pkAttr, skAttr, keys.GSI1PKAttr, keys.GSI1SKAttr:
			s, ok := v.(*types.AttributeValueMemberS)
			if !ok {
				return Item{}, fmt.Errorf("repository: stored attribute %q is %T, expected a string", k, v)
			}
			switch k {
			case pkAttr:
				item.Key.PK = s.Value
			case skAttr:
				item.Key.SK = s.Value
			case keys.GSI1PKAttr:
				item.GSI1PK = s.Value
			case keys.GSI1SKAttr:
				item.GSI1SK = s.Value
			}
			continue
		case ttlAttr:
			n, ok := v.(*types.AttributeValueMemberN)
			if !ok {
				return Item{}, fmt.Errorf("repository: stored attribute %q is %T, expected a number", k, v)
			}
			ttl, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil {
				return Item{}, fmt.Errorf("repository: stored attribute %q is %q, not an epoch second: %w", ttlAttr, n.Value, err)
			}
			item.TTL = ttl
			continue
		}
		val, err := unmarshalValue(v)
		if err != nil {
			return Item{}, fmt.Errorf("repository: stored attribute %q: %w", k, err)
		}
		attrs[k] = val
	}
	if item.Key.PK == "" || item.Key.SK == "" {
		// A decode-time invariant, and **not** an I16 enforcement point — an earlier
		// version of this comment claimed it was, and that claim was false. PK and SK are
		// the table's HASH and RANGE keys, both declared AttributeType: S in
		// infrastructure/template.yaml, so DynamoDB cannot store an item missing either
		// and cannot return one from GetItem or from a base-table Query; an ad-hoc
		// `aws dynamodb put-item` must supply both as well. Nothing here detects a record
		// written outside this adapter, and nobody assessing I16 coverage should count it.
		//
		// It is kept for what it does do: it is the only thing guaranteeing that every
		// Item this package hands out carries a usable key, which callers rely on when
		// they build a follow-up write from got.Key. It becomes reachable the moment a
		// ProjectionExpression or an index read is added — which is exactly the change
		// after which a silently-empty Key would be written back into a record.
		return Item{}, fmt.Errorf("repository: stored record is missing %s/%s; every Item this package returns must carry a usable key", pkAttr, skAttr)
	}
	if len(attrs) > 0 {
		// nil for a record with no non-reserved attributes, matching the fake. DynamoDB
		// stores no attribute for an empty map, so "Attrs was nil" and "Attrs was an empty
		// map" arrive here identically and the adapter cannot tell them apart — assigning
		// a non-nil empty map anyway made `if item.Attrs == nil` take one branch in every
		// test and the opposite branch in production. memory.go's copyItem normalises the
		// same way, so it is one rule for both: zero attributes reads back as a nil map.
		item.Attrs = attrs
	}
	return item, nil
}

// unmarshalValue converts one stored attribute value.
//
// Collections come back as []any and map[string]any regardless of what Go type produced
// them, because DynamoDB stores no element type: a []string goes out as a list of strings
// and comes back as []any. **This is the one divergence from the fake that a caller can
// trip over** — the fake returns the []string it was given — so a caller reading a list
// attribute must read it as []any, and a test asserting []string will pass against the
// fake and fail in production. There is no fix available on this side: the type
// information does not exist in the stored record.
func unmarshalValue(v types.AttributeValue) (any, error) {
	switch t := v.(type) {
	case *types.AttributeValueMemberS:
		return t.Value, nil
	case *types.AttributeValueMemberBOOL:
		return t.Value, nil
	case *types.AttributeValueMemberNULL:
		return nil, nil
	case *types.AttributeValueMemberB:
		return t.Value, nil
	case *types.AttributeValueMemberN:
		return unmarshalNumber(t.Value)
	case *types.AttributeValueMemberL:
		out := make([]any, len(t.Value))
		for i, e := range t.Value {
			val, err := unmarshalValue(e)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = val
		}
		return out, nil
	case *types.AttributeValueMemberM:
		out := make(map[string]any, len(t.Value))
		for k, e := range t.Value {
			val, err := unmarshalValue(e)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k, err)
			}
			out[k] = val
		}
		return out, nil
	// The set types are never written by this adapter. They are read anyway: the
	// tenant-erasure path (§9.3) and the export path (§Phase 7) must be able to read
	// every record in the table, and a record they cannot decode is one they cannot
	// erase. Decoded as lists for the same reason lists are — the element type is not
	// recoverable.
	case *types.AttributeValueMemberSS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			out[i] = s
		}
		return out, nil
	case *types.AttributeValueMemberNS:
		out := make([]any, len(t.Value))
		for i, s := range t.Value {
			n, err := unmarshalNumber(s)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = n
		}
		return out, nil
	case *types.AttributeValueMemberBS:
		out := make([]any, len(t.Value))
		for i, b := range t.Value {
			out[i] = b
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported stored attribute type %T", v)
}

// unmarshalNumber decodes a DynamoDB number.
//
// Integral values within int64 become int64 and everything else float64. This is what
// keeps money exact: meter reads cost_micros through AsInt64, and float64 cannot represent
// every int64. The reciprocal hazard is real and is why AsFloat64 exists — a float64
// attribute whose value happens to be whole (a quantity of 3.0) is stored as "3" and comes
// back as int64(3), where the fake returns float64(3). A direct .(float64) assertion on
// such an attribute silently yields zero in production and works in tests.
func unmarshalNumber(s string) (any, error) {
	i, err := strconv.ParseInt(s, 10, 64)
	if err == nil {
		return i, nil
	}
	if errors.Is(err, strconv.ErrRange) {
		// An integer decimal beyond int64. **Decoded as float64 rather than refused, and
		// the earlier refusal here was the worst bug in this package.** It was raised from
		// unmarshalItem, so it poisoned the whole record and, through QueryPrefix, the
		// whole partition: one usage record carrying a quantity of 1e19 — a legal
		// meter.Event, since Event.Quantity is a float64 bounded only from below — made
		// every MonthTotal and DayTotal for that tenant-month fail, which breaker turns
		// into ErrSpendUnknown and a blanket refusal of every capture for the rest of the
		// month. QueryPrefix could not even list the partition, so the offending key was
		// not discoverable through any sanctioned path, and repair needed the ad-hoc CLI
		// that I16 forbids. The same reasoning as the set-type branch of unmarshalValue
		// applies with more force: §9.3 erasure and §Phase 7 export must be able to read
		// every record, and a record they cannot decode is one they cannot erase.
		//
		// Money is still exact, because the precision guard belongs at the read of the
		// attribute that is money and not here. Nothing this adapter writes reaches this
		// branch — marshalValue refuses a uint64 above MaxInt64 and every other integral
		// path is an int64 — and AsInt64 returns false for any float64 at or beyond 2^53,
		// so meter.costMicros refuses that one attribute instead of rounding it. A scoped
		// refusal the caller can act on, rather than an unreadable ledger.
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			// Beyond float64 as well, so there is no representation left to return.
			// DynamoDB's own numeric range stops at ~1e126, so this cannot be a stored
			// value at all — it is a malformed response or a hand-edited record.
			return nil, fmt.Errorf("number %q exceeds int64 and the range of float64, so it cannot be decoded at all", s)
		}
		return f, nil
	}
	f, ferr := strconv.ParseFloat(s, 64)
	if ferr != nil {
		return nil, fmt.Errorf("number %q is not a valid number: %w", s, ferr)
	}
	return f, nil
}

// maxExactInFloat64 is 2^53: above it, consecutive integers are not distinguishable as
// float64, so a conversion is a silent loss rather than a rounding.
const maxExactInFloat64 = 1 << 53

// AsInt64 reads an integral number from an Attrs value.
//
// It exists because the two Repository implementations cannot agree on the Go type of a
// whole number — see unmarshalNumber — so an attribute read with a direct type assertion
// can work against the fake and fail against DynamoDB. Reading numbers through AsInt64
// and AsFloat64 is the only way a caller gets the same answer from both. Returns false
// rather than a rounded value when the conversion would lose precision.
func AsInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case float64:
		if n != math.Trunc(n) || n >= maxExactInFloat64 || n <= -maxExactInFloat64 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// AsFloat64 reads a number from an Attrs value, accepting either representation.
func AsFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}
