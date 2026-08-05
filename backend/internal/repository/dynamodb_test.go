package repository

// These tests never reach AWS. What they can prove without a table is everything that
// lives in this package rather than in the service: error translation, marshalling
// fidelity (including that money never passes through a float), pagination assembly, and
// the shape of the requests sent. What they cannot prove is stated in the package report
// — chiefly that DynamoDB actually honours attribute_not_exists the way PutOnce assumes,
// and that a real 1MB page boundary produces the LastEvaluatedKey this code loops on.
//
// The negative cases carry the weight. A PutOnce that returns ErrAlreadyExists for a
// throttle would make an idempotent retry report success while nothing was written, and a
// QueryPrefix that stops at the first page returns a plausible, wrong number. Neither is
// visible in a positive test.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/keys"
)

const testTenant = keys.TenantID("t_test")

// mustKey unwraps a keys constructor. It panics rather than taking a *testing.T,
// because a key the keys package refuses means the test itself is wrong, and because a
// two-value call cannot be spread into a helper that also takes t.
func mustKey(k keys.DynamoKey, err error) keys.DynamoKey {
	if err != nil {
		panic(fmt.Sprintf("test: building a key: %v", err))
	}
	return k
}

// ---------------------------------------------------------------------------
// stubDynamo — canned responses, for error translation and request shape
// ---------------------------------------------------------------------------

type stubDynamo struct {
	getOut *dynamodb.GetItemOutput
	getErr error
	gets   []*dynamodb.GetItemInput

	putErr error
	puts   []*dynamodb.PutItemInput

	deleteErr error
	deletes   []*dynamodb.DeleteItemInput

	// queryPages is returned one per call, so pagination is exercised without a real
	// 1MB boundary. queryErr fails the call at index queryErrAt.
	queryPages []*dynamodb.QueryOutput
	queryErr   error
	queryErrAt int
	queries    []*dynamodb.QueryInput
}

func (s *stubDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	s.gets = append(s.gets, in)
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.getOut != nil {
		return s.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (s *stubDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	s.puts = append(s.puts, in)
	if s.putErr != nil {
		return nil, s.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (s *stubDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	s.deletes = append(s.deletes, in)
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	return &dynamodb.DeleteItemOutput{}, nil
}

func (s *stubDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	idx := len(s.queries)
	s.queries = append(s.queries, in)
	if s.queryErr != nil && idx == s.queryErrAt {
		return nil, s.queryErr
	}
	if idx < len(s.queryPages) {
		return s.queryPages[idx], nil
	}
	return &dynamodb.QueryOutput{}, nil
}

func newStubbed(t *testing.T, stub *stubDynamo) *Dynamo {
	t.Helper()
	d, err := NewDynamo(stub, "voicenotes-test")
	if err != nil {
		t.Fatalf("NewDynamo: %v", err)
	}
	return d
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewDynamoRefusesUnusableConfiguration(t *testing.T) {
	cases := map[string]struct {
		api    DynamoAPI
		table  string
		expect string
	}{
		// An empty table name would reach the SDK and come back as a validation error
		// from AWS on the first request rather than as a configuration error at cold
		// start, so the message has to name the variable that is missing.
		"empty table": {api: &stubDynamo{}, table: "", expect: "CHINTAN_TABLE"},
		"nil api":     {api: nil, table: "voicenotes-test", expect: "nil"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewDynamo(tc.api, tc.table)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("error %q does not mention %q, so it is not actionable", err, tc.expect)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error translation
// ---------------------------------------------------------------------------

func TestPutOnceTranslatesOnlyTheConditionalCheckFailure(t *testing.T) {
	key := mustKey(keys.Audit(testTenant, "01HAUDIT"))
	item := Item{Key: key, Attrs: map[string]any{"action": "read"}}

	cases := map[string]struct {
		from error
		// wantAlreadyExists is the crux. Translating anything other than the condition
		// failure to ErrAlreadyExists makes an idempotency retry conclude "the first
		// attempt already wrote it" when nothing was written, so the capture is lost and
		// the client is told it succeeded (§2A.1).
		wantAlreadyExists bool
	}{
		"conditional check failed": {
			from:              &types.ConditionalCheckFailedException{Message: aws.String("The conditional request failed")},
			wantAlreadyExists: true,
		},
		"conditional check failed, wrapped": {
			from:              fmt.Errorf("operation error DynamoDB: PutItem: %w", &types.ConditionalCheckFailedException{}),
			wantAlreadyExists: true,
		},
		"throttled": {
			from:              &types.ProvisionedThroughputExceededException{},
			wantAlreadyExists: false,
		},
		"request limit exceeded": {
			from:              &types.RequestLimitExceeded{},
			wantAlreadyExists: false,
		},
		"table missing": {
			from:              &types.ResourceNotFoundException{},
			wantAlreadyExists: false,
		},
		"transport failure": {
			from:              errors.New("dial tcp: i/o timeout"),
			wantAlreadyExists: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := newStubbed(t, &stubDynamo{putErr: tc.from})
			err := d.PutOnce(context.Background(), item)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errors.Is(err, ErrAlreadyExists); got != tc.wantAlreadyExists {
				t.Errorf("errors.Is(err, ErrAlreadyExists) = %v, want %v (err = %v)", got, tc.wantAlreadyExists, err)
			}
			// A table-missing error must never read as "not found for this key": the
			// caller would return 404 for a resource that exists in a table that does
			// not, and the deployment fault would look like a data fault.
			if errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v; ErrNotFound is only for a missing item", err)
			}
		})
	}
}

func TestPutOnceConditionsOnThePartitionKey(t *testing.T) {
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	key := mustKey(keys.Idempotency(testTenant, "idem-1"))
	if err := d.PutOnce(context.Background(), Item{Key: key, Attrs: map[string]any{"a": "b"}}); err != nil {
		t.Fatalf("PutOnce: %v", err)
	}
	if len(stub.puts) != 1 {
		t.Fatalf("got %d PutItem calls, want 1", len(stub.puts))
	}
	in := stub.puts[0]
	if in.ConditionExpression == nil {
		t.Fatal("PutOnce sent no ConditionExpression; without it a second write overwrites the first and idempotency does nothing")
	}
	if got := *in.ConditionExpression; got != "attribute_not_exists(#pk)" {
		t.Errorf("ConditionExpression = %q, want attribute_not_exists on the partition key", got)
	}
	// The name must resolve to the real partition key attribute. A condition on any
	// other attribute is satisfied by an existing item and lets the write through.
	if got := in.ExpressionAttributeNames["#pk"]; got != pkAttr {
		t.Errorf("#pk resolves to %q, want %q", got, pkAttr)
	}
}

func TestPutSetsNoConditionSoItCanReplace(t *testing.T) {
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	key := mustKey(keys.Capture(testTenant, "c_1"))
	if err := d.Put(context.Background(), Item{Key: key, Attrs: map[string]any{"label": "x"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Put is the mutable-entity path. A condition here would make the second edit of a
	// capture fail, and the failure would look like a permissions problem.
	if stub.puts[0].ConditionExpression != nil {
		t.Errorf("Put sent ConditionExpression %q; Put must replace", *stub.puts[0].ConditionExpression)
	}
}

func TestGetReturnsErrNotFoundForAMissingItem(t *testing.T) {
	// DynamoDB reports a miss as an empty attribute map and a nil error. A caller that
	// received (nil, nil) would carry on with a zero-valued record.
	d := newStubbed(t, &stubDynamo{getOut: &dynamodb.GetItemOutput{}})
	got, err := d.Get(context.Background(), mustKey(keys.Capture(testTenant, "c_missing")))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Errorf("item = %+v, want nil alongside ErrNotFound", got)
	}
}

func TestGetDoesNotTurnATransportFailureIntoNotFound(t *testing.T) {
	// A 404 for a throttled read is how a transient fault becomes a "your capture is
	// gone" in the UI, and the caller cannot distinguish it in order to retry.
	d := newStubbed(t, &stubDynamo{getErr: &types.ProvisionedThroughputExceededException{}})
	_, err := d.Get(context.Background(), mustKey(keys.Capture(testTenant, "c_1")))
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v; a throttle must not read as ErrNotFound", err)
	}
}

func TestGetUsesAConsistentRead(t *testing.T) {
	stub := &stubDynamo{getOut: &dynamodb.GetItemOutput{Item: map[string]types.AttributeValue{
		pkAttr: &types.AttributeValueMemberS{Value: "x"},
		skAttr: &types.AttributeValueMemberS{Value: "y"},
	}}}
	d := newStubbed(t, stub)
	if _, err := d.Get(context.Background(), mustKey(keys.Capture(testTenant, "c_1"))); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The fake is immediately consistent. An eventually-consistent Get makes
	// read-after-write pass in every test and fail intermittently in production, which
	// is the failure mode that costs a transcript.
	if cr := stub.gets[0].ConsistentRead; cr == nil || !*cr {
		t.Error("Get did not request a consistent read")
	}
	if got := aws.ToString(stub.gets[0].TableName); got != "voicenotes-test" {
		t.Errorf("TableName = %q, want the name passed to NewDynamo", got)
	}
}

// ---------------------------------------------------------------------------
// Money and number fidelity
// ---------------------------------------------------------------------------

func TestMoneyIsStoredAsAnExactDecimalInteger(t *testing.T) {
	// 2^53+1 is the smallest positive integer float64 cannot represent. If any part of
	// the path goes through a float, this value comes back one lower, and no later code
	// can tell that it happened.
	const beyondFloat64 = int64(1)<<53 + 1

	cases := map[string]struct {
		in   int64
		want string
	}{
		"zero":                  {0, "0"},
		"one micro":             {1, "1"},
		"beyond float64":        {beyondFloat64, "9007199254740993"},
		"max int64":             {math.MaxInt64, "9223372036854775807"},
		"large realistic spend": {123456789, "123456789"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			key := mustKey(keys.Usage(testTenant, "2026-08", "stt_seconds", "01HUSAGE"))
			av, err := marshalItem(Item{Key: key, Attrs: map[string]any{"cost_micros": tc.in}})
			if err != nil {
				t.Fatalf("marshalItem: %v", err)
			}
			n, ok := av["cost_micros"].(*types.AttributeValueMemberN)
			if !ok {
				t.Fatalf("cost_micros stored as %T, want a DynamoDB number", av["cost_micros"])
			}
			if n.Value != tc.want {
				t.Errorf("cost_micros wire value = %q, want %q — money must not round-trip through float64 (§Phase 0 reconciliation)", n.Value, tc.want)
			}
			back, err := unmarshalItem(av)
			if err != nil {
				t.Fatalf("unmarshalItem: %v", err)
			}
			// meter.MonthTotal asserts .(int64) directly, so the concrete type matters
			// as much as the value: a float64 here sums to zero and the spend breaker
			// under-counts.
			got, ok := back.Attrs["cost_micros"].(int64)
			if !ok {
				t.Fatalf("cost_micros read back as %T, want int64 (meter asserts int64 directly)", back.Attrs["cost_micros"])
			}
			if got != tc.in {
				t.Errorf("cost_micros = %d, want %d", got, tc.in)
			}
		})
	}
}

func TestNumberRoundTripClosureHolds(t *testing.T) {
	// The property, not an example of it: every value the write path accepts must read back
	// through the read path equal to what was written. The first version of this code broke
	// closure — marshalFloat emitted "10000000000000000000" for 1e19 and the Uint64 branch
	// emitted 20-digit strings, both of which unmarshalNumber then refused, and it refused
	// them for the whole item — so one usage record made an entire tenant-month of usage
	// unreadable. No example-based test found it in 122 subtests, and the fake cannot find
	// it at all: it stores the exact Go value it was handed, so the two halves of this
	// adapter are the only place the asymmetry exists.
	cases := map[string]any{
		"zero":                   int64(0),
		"one":                    int64(1),
		"max int64":              int64(math.MaxInt64),
		"min int64":              int64(math.MinInt64),
		"beyond float64":         int64(1)<<53 + 1,
		"plain int":              42,
		"int32":                  int32(-7),
		"uint64 at max int64":    uint64(math.MaxInt64),
		"uint32":                 uint32(4294967295),
		"float fraction":         0.87,
		"float negative":         -0.001,
		"float whole":            float64(3),
		"float 1e19":             1e19, // the reviewer's reproduction, via meter.Event.Quantity
		"float 1e30":             1e30,
		"float tiny":             1e-9,
		"float at 2^53":          float64(1 << 53),
		"float32":                float32(0.5),
		"string":                 "capture",
		"empty string":           "",
		"bool":                   true,
		"bytes":                  []byte{0x00, 0xff},
		"nil":                    nil,
		"list":                   []any{int64(1), "a", 2.5},
		"map":                    map[string]any{"a": int64(1)},
		"list holding a big int": []any{1e19},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			av, err := marshalValue(in)
			if err != nil {
				t.Fatalf("marshalValue(%#v): %v — the write path accepted nothing, so closure is not the question", in, err)
			}
			back, err := unmarshalValue(av)
			if err != nil {
				t.Fatalf("unmarshalValue after marshalValue(%#v): %v — the write path emitted something the read path refuses, which is the bug that made a whole partition unreadable", in, err)
			}
			if !sameNumberOrValue(in, back) {
				t.Errorf("round trip of %#v (%T) produced %#v (%T)", in, in, back, back)
			}
		})
	}
}

// sameNumberOrValue compares a written value with what came back.
//
// Numbers are compared by value rather than by Go type, because the read path deliberately
// normalises: DynamoDB has one number type, so an int32 and an integral float both come
// back as int64. Two integers are compared as integers rather than through float64, or the
// money cases would pass for free — 2^53+1 and 2^53 are the same float64, which is the
// exact confusion this package exists to prevent.
func sameNumberOrValue(want, got any) bool {
	wi, wIsInt := exactInt(want)
	gi, gIsInt := exactInt(got)
	switch {
	case wIsInt && gIsInt:
		return wi == gi
	case isNumber(want) && isNumber(got):
		wf, _ := anyFloat(want)
		gf, _ := anyFloat(got)
		return wf == gf
	case isNumber(want) != isNumber(got):
		return false
	}
	switch w := want.(type) {
	case nil:
		return got == nil
	case []byte:
		g, ok := got.([]byte)
		return ok && string(w) == string(g)
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !sameNumberOrValue(w[i], g[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k := range w {
			if !sameNumberOrValue(w[k], g[k]) {
				return false
			}
		}
		return true
	default:
		return want == got
	}
}

// exactInt reports an integer value without going through float64, so a comparison of two
// integers is exact at every magnitude int64 can hold.
func exactInt(v any) (int64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if u := rv.Uint(); u <= math.MaxInt64 {
			return int64(u), true
		}
	}
	return 0, false
}

func isNumber(v any) bool {
	_, ok := anyFloat(v)
	return ok
}

func anyFloat(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

func TestNumberBeyondInt64DecodesRatherThanPoisoningTheRecord(t *testing.T) {
	// A number beyond int64 cannot be written by this adapter — see the uint64 case in
	// TestMarshalRefusesValuesItCannotStoreFaithfully — so one in the table came from
	// outside it. It is decoded anyway: refusing it failed the whole item, and through
	// QueryPrefix the whole partition, which is how a single usage record refused every
	// capture for a tenant-month. §9.3 erasure and §Phase 7 export must be able to read
	// every record.
	cases := map[string]struct {
		stored string
		want   float64
	}{
		"max int64 plus one":  {"9223372036854775808", 9223372036854775808},
		"min int64 minus one": {"-9223372036854775809", -9223372036854775809},
		"38 nines":            {"99999999999999999999999999999999999999", 1e38},
		"integral 1e19":       {"10000000000000000000", 1e19},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := unmarshalNumber(tc.stored)
			if err != nil {
				t.Fatalf("unmarshalNumber(%q) = %v; an undecodable record is one the erasure path cannot erase", tc.stored, err)
			}
			f, ok := got.(float64)
			if !ok {
				t.Fatalf("decoded as %T, want float64", got)
			}
			if f != tc.want {
				t.Errorf("decoded %v, want %v", f, tc.want)
			}
			// And the money guarantee still holds, scoped to the attribute that is money:
			// meter.costMicros reads through AsInt64 precisely so a value it cannot
			// represent exactly is refused rather than rounded into a spend total.
			if n, ok := AsInt64(got); ok {
				t.Errorf("AsInt64 accepted %v as %d; a cost beyond int64 must be refused, not rounded into a spend total", f, n)
			}
		})
	}
}

func TestNumberBeyondFloat64IsStillRefused(t *testing.T) {
	// Past float64 there is no representation left to return, so decoding fails — but this
	// is not a value DynamoDB can hold (its own range stops near 1e126), so it means a
	// malformed response or a hand-edited record rather than a stored quantity.
	for _, s := range []string{"1" + strings.Repeat("0", 400), "-1" + strings.Repeat("0", 400)} {
		if _, err := unmarshalNumber(s); err == nil {
			t.Errorf("unmarshalNumber(%q…) was accepted", s[:12])
		}
	}
}

func TestFloatsAreStoredWithoutLossAndNonFinitesRefused(t *testing.T) {
	t.Run("finite floats round-trip", func(t *testing.T) {
		for _, f := range []float64{0.5, 1.25, 28.734, -0.001, 1e15} {
			av, err := marshalValue(f)
			if err != nil {
				t.Fatalf("marshalValue(%v): %v", f, err)
			}
			back, err := unmarshalValue(av)
			if err != nil {
				t.Fatalf("unmarshalValue: %v", err)
			}
			got, ok := AsFloat64(back)
			if !ok {
				t.Fatalf("%v read back as %T", f, back)
			}
			if got != f {
				t.Errorf("float %v round-tripped to %v", f, got)
			}
		}
	})
	t.Run("non-finite refused", func(t *testing.T) {
		// DynamoDB has no representation for these; without this check the failure
		// arrives as an opaque validation error from the service.
		for _, f := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			if _, err := marshalValue(f); err == nil {
				t.Errorf("marshalValue(%v) was accepted", f)
			}
		}
	})
}

func TestWholeFloatComesBackAsInt64AndTheAccessorsBridgeIt(t *testing.T) {
	// The reciprocal of the money rule, asserted so the divergence from the fake is a
	// recorded decision rather than a surprise: meter writes quantity as a float64, and
	// a quantity of exactly 3 is stored as "3" and read back as int64(3) where the fake
	// returns float64(3). A direct .(float64) assertion silently yields zero.
	av, err := marshalValue(float64(3))
	if err != nil {
		t.Fatalf("marshalValue: %v", err)
	}
	back, err := unmarshalValue(av)
	if err != nil {
		t.Fatalf("unmarshalValue: %v", err)
	}
	if _, isFloat := back.(float64); isFloat {
		t.Fatal("a whole float came back as float64; if this now holds, AsFloat64's reason for existing has changed and the comment must be corrected")
	}
	if f, ok := AsFloat64(back); !ok || f != 3 {
		t.Errorf("AsFloat64 = (%v, %v), want (3, true) — this accessor is what makes both back ends agree", f, ok)
	}
}

func TestAsInt64RefusesLossyConversions(t *testing.T) {
	cases := map[string]struct {
		in   any
		want int64
		ok   bool
	}{
		"int64":                  {int64(7), 7, true},
		"int":                    {7, 7, true},
		"whole float":            {float64(7), 7, true},
		"fractional float":       {float64(7.5), 0, false},
		"float beyond 2^53":      {float64(1 << 54), 0, false},
		"string":                 {"7", 0, false},
		"nil":                    {nil, 0, false},
		"bool":                   {true, 0, false},
		"negative whole float":   {float64(-7), -7, true},
		"negative beyond 2^53":   {-float64(1 << 54), 0, false},
		"float that is 2^53":     {float64(1 << 53), 0, false},
		"float just below 2^53":  {float64(1<<53 - 1), 1<<53 - 1, true},
		"list is not a number":   {[]any{1}, 0, false},
		"map is not a number":    {map[string]any{}, 0, false},
		"bytes are not a number": {[]byte{1}, 0, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := AsInt64(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("AsInt64(%#v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Marshalling: reserved attributes, TTL, GSI1
// ---------------------------------------------------------------------------

func TestMarshalRejectsKeyAttributesSuppliedThroughAttrs(t *testing.T) {
	// A caller setting these in Attrs expects them honoured; overwriting them silently
	// from the struct fields would store a record under a key the caller did not intend,
	// and a hand-assembled key attribute is exactly what check-tenant-keys.sh forbids.
	key := mustKey(keys.Capture(testTenant, "c_1"))
	for _, name := range []string{pkAttr, skAttr, keys.GSI1PKAttr, keys.GSI1SKAttr} {
		t.Run(name, func(t *testing.T) {
			_, err := marshalItem(Item{Key: key, Attrs: map[string]any{name: "forged"}})
			if err == nil {
				t.Fatalf("Attrs[%q] was accepted", name)
			}
			if !strings.Contains(err.Error(), "key attribute") {
				t.Errorf("error %q does not explain the refusal", err)
			}
		})
	}
}

func TestTTLAttributeIsAbsentWhenThereIsNoExpiry(t *testing.T) {
	// A ttl of 0 is epoch 1970, so writing the zero value would mark the item
	// immediately eligible for deletion. Every mutable entity has TTL == 0.
	key := mustKey(keys.Capture(testTenant, "c_1"))
	av, err := marshalItem(Item{Key: key, Attrs: map[string]any{"label": "x"}})
	if err != nil {
		t.Fatalf("marshalItem: %v", err)
	}
	if _, present := av[ttlAttr]; present {
		t.Fatalf("%q attribute written for an item with TTL 0; DynamoDB would expire it immediately", ttlAttr)
	}
	back, err := unmarshalItem(av)
	if err != nil {
		t.Fatalf("unmarshalItem: %v", err)
	}
	if back.TTL != 0 {
		t.Errorf("TTL = %d, want 0", back.TTL)
	}
	if _, present := back.Attrs[ttlAttr]; present {
		t.Errorf("%q leaked into Attrs on read", ttlAttr)
	}
}

func TestTTLIsTakenFromTheStructFieldAndDisagreementIsRefused(t *testing.T) {
	key := mustKey(keys.Usage(testTenant, "2026-08", "stt_seconds", "01HUSAGE"))
	const expiry = int64(1_800_000_000)

	t.Run("meter's shape is accepted", func(t *testing.T) {
		// meter.Record writes ttl in both places, deliberately (§6.3 lists it as an
		// attribute of the usage record). That shape must not be an error.
		av, err := marshalItem(Item{
			Key:   key,
			Attrs: map[string]any{"cost_micros": int64(5), "ttl": expiry},
			TTL:   expiry,
		})
		if err != nil {
			t.Fatalf("marshalItem: %v", err)
		}
		n, ok := av[ttlAttr].(*types.AttributeValueMemberN)
		if !ok || n.Value != "1800000000" {
			t.Fatalf("%q = %#v, want the epoch second as a number", ttlAttr, av[ttlAttr])
		}
		back, err := unmarshalItem(av)
		if err != nil {
			t.Fatalf("unmarshalItem: %v", err)
		}
		if back.TTL != expiry {
			t.Errorf("TTL = %d, want %d", back.TTL, expiry)
		}
	})

	t.Run("disagreement refused", func(t *testing.T) {
		cases := map[string]Item{
			// The dangerous one: Attrs carries an expiry, the field does not, so the
			// record would be retained forever and nobody would notice.
			"attrs only":      {Key: key, Attrs: map[string]any{"ttl": expiry}, TTL: 0},
			"different value": {Key: key, Attrs: map[string]any{"ttl": expiry + 1}, TTL: expiry},
			"wrong type":      {Key: key, Attrs: map[string]any{"ttl": "1800000000"}, TTL: expiry},
		}
		for name, item := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := marshalItem(item); err == nil {
					t.Error("accepted; guessing which value the caller meant either expires a record early or never expires it, and both are silent")
				}
			})
		}
	})

	t.Run("negative refused", func(t *testing.T) {
		if _, err := marshalItem(Item{Key: key, TTL: -1}); err == nil {
			t.Error("a negative TTL was accepted")
		}
	})
}

func TestGSI1AttributesAreWrittenAsAPairOrNotAtAll(t *testing.T) {
	capture := mustKey(keys.Capture(testTenant, "c_1"))
	gsiPK, gsiSK, err := keys.GSI1(testTenant, "2026-08-04T10:00:00Z")
	if err != nil {
		t.Fatalf("keys.GSI1: %v", err)
	}

	t.Run("absent for the high-volume entities", func(t *testing.T) {
		// Segment, Usage, Audit, and Metric must never project into GSI1 or the index
		// becomes a second copy of the table (§6.3) — paid for twice at on-demand rates.
		seg := mustKey(keys.Segment(testTenant, "c_1", 0))
		av, err := marshalItem(Item{Key: seg, Attrs: map[string]any{"block_id": "t-0001"}})
		if err != nil {
			t.Fatalf("marshalItem: %v", err)
		}
		for _, name := range []string{keys.GSI1PKAttr, keys.GSI1SKAttr} {
			if _, present := av[name]; present {
				t.Errorf("%q written for a segment record; the index must stay sparse", name)
			}
		}
	})

	t.Run("both present round-trips", func(t *testing.T) {
		av, err := marshalItem(Item{Key: capture, GSI1PK: gsiPK, GSI1SK: gsiSK, Attrs: map[string]any{"label": "x"}})
		if err != nil {
			t.Fatalf("marshalItem: %v", err)
		}
		back, err := unmarshalItem(av)
		if err != nil {
			t.Fatalf("unmarshalItem: %v", err)
		}
		if back.GSI1PK != gsiPK || back.GSI1SK != gsiSK {
			t.Errorf("GSI1 = (%q, %q), want (%q, %q)", back.GSI1PK, back.GSI1SK, gsiPK, gsiSK)
		}
		if _, leaked := back.Attrs[keys.GSI1PKAttr]; leaked {
			t.Error("GSI1PK leaked into Attrs on read")
		}
	})

	t.Run("one alone refused", func(t *testing.T) {
		// DynamoDB accepts this and simply does not project the item, so the capture
		// exists and never appears in the time-ordered listing. There is no service
		// error to catch, which is why it is refused here.
		for name, item := range map[string]Item{
			"pk only": {Key: capture, GSI1PK: gsiPK},
			"sk only": {Key: capture, GSI1SK: gsiSK},
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := marshalItem(item); err == nil {
					t.Error("accepted; the record would silently never appear in GSI1")
				}
			})
		}
	})
}

func TestMarshalRoundTripsEverySupportedValueShape(t *testing.T) {
	key := mustKey(keys.Item(testTenant, "i_1"))
	item := Item{
		Key: key,
		Attrs: map[string]any{
			"text":        "LZ4 decode-only looks like the practical choice",
			"kind":        "idea",
			"confidence":  0.87,
			"pinned":      true,
			"archived":    false,
			"absent":      nil,
			"digest":      []byte{0xde, 0xad, 0xbe, 0xef},
			"count":       int64(3),
			"plain_int":   42,
			"empty_list":  []any{},
			"empty_map":   map[string]any{},
			"nested":      map[string]any{"a": int64(1), "b": []any{"x", "y"}},
			"empty_value": "",
		},
	}
	av, err := marshalItem(item)
	if err != nil {
		t.Fatalf("marshalItem: %v", err)
	}
	back, err := unmarshalItem(av)
	if err != nil {
		t.Fatalf("unmarshalItem: %v", err)
	}
	if back.Key != key {
		t.Errorf("key = %+v, want %+v", back.Key, key)
	}
	checks := map[string]any{
		"text":        "LZ4 decode-only looks like the practical choice",
		"kind":        "idea",
		"confidence":  0.87,
		"pinned":      true,
		"archived":    false,
		"absent":      nil,
		"count":       int64(3),
		"plain_int":   int64(42), // int normalises to int64; DynamoDB has one number type
		"empty_value": "",
	}
	for name, want := range checks {
		if got := back.Attrs[name]; got != want {
			t.Errorf("Attrs[%q] = %#v, want %#v", name, got, want)
		}
	}
	if got, ok := back.Attrs["digest"].([]byte); !ok || string(got) != "\xde\xad\xbe\xef" {
		t.Errorf("Attrs[digest] = %#v, want the original bytes", back.Attrs["digest"])
	}
	if got, ok := back.Attrs["empty_list"].([]any); !ok || len(got) != 0 {
		t.Errorf("Attrs[empty_list] = %#v, want an empty []any", back.Attrs["empty_list"])
	}
	nested, ok := back.Attrs["nested"].(map[string]any)
	if !ok {
		t.Fatalf("Attrs[nested] = %T, want map[string]any", back.Attrs["nested"])
	}
	if nested["a"] != int64(1) {
		t.Errorf("nested[a] = %#v, want int64(1)", nested["a"])
	}
	inner, ok := nested["b"].([]any)
	if !ok || len(inner) != 2 || inner[0] != "x" {
		t.Errorf("nested[b] = %#v, want []any{\"x\", \"y\"}", nested["b"])
	}
}

func TestTypedCollectionsNormaliseToAnyOnRead(t *testing.T) {
	// Recorded because it is the divergence from the fake a caller is most likely to hit:
	// the fake returns the []string it was given, this returns []any, and DynamoDB stores
	// no element type so there is no third option. A test asserting []string passes
	// against the fake and fails in production.
	key := mustKey(keys.Rule(testTenant, "TNSTRNT"))
	av, err := marshalItem(Item{Key: key, Attrs: map[string]any{
		"variants": []string{"tenstorrent", "ten storrent"},
		"hits":     map[string]int{"a": 2},
	}})
	if err != nil {
		t.Fatalf("marshalItem: %v", err)
	}
	back, err := unmarshalItem(av)
	if err != nil {
		t.Fatalf("unmarshalItem: %v", err)
	}
	if _, isTyped := back.Attrs["variants"].([]string); isTyped {
		t.Fatal("a []string survived the round trip; if that now holds, the comment on unmarshalValue is wrong")
	}
	list, ok := back.Attrs["variants"].([]any)
	if !ok || len(list) != 2 || list[0] != "tenstorrent" {
		t.Errorf("Attrs[variants] = %#v, want []any of the same strings", back.Attrs["variants"])
	}
	m, ok := back.Attrs["hits"].(map[string]any)
	if !ok || m["a"] != int64(2) {
		t.Errorf("Attrs[hits] = %#v, want map[string]any{\"a\": int64(2)}", back.Attrs["hits"])
	}
}

func TestMarshalRefusesValuesItCannotStoreFaithfully(t *testing.T) {
	key := mustKey(keys.Item(testTenant, "i_1"))
	cases := map[string]any{
		// A reflected reduction would drop the unexported field without saying so, and a
		// record that comes back missing something the writer believed it stored is not
		// recoverable from the record.
		"struct with unexported state": opaque{Shown: "x"},
		"slice holding one":            []any{opaque{}},
		"non-string-key map":           map[int]string{1: "a"},
		"channel":                      make(chan int),
		"func":                         func() {},
		// Written as a 20-digit decimal it would read back one higher, because the read
		// path decodes an integer beyond int64 as float64. Refused at the call site that
		// still holds the exact value rather than stored as a value nobody wrote.
		"uint64 beyond int64": uint64(math.MaxUint64),
		"uint64 just beyond":  uint64(math.MaxInt64) + 1,
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := marshalItem(Item{Key: key, Attrs: map[string]any{"x": v}}); err == nil {
				t.Error("accepted a value that cannot be represented faithfully")
			}
			// And the fake refuses the same value, because validateItem asks this same
			// marshaller. A value the fake accepts and production rejects is a write that
			// works in every test and fails in front of a user.
			if err := NewMemory().Put(context.Background(), Item{Key: key, Attrs: map[string]any{"x": v}}); err == nil {
				t.Error("the fake accepted a value the adapter cannot store")
			}
		})
	}
}

// opaque carries unexported state, so a reflected reduction would lose part of it.
// time.Time is the shape this stands in for.
type opaque struct {
	Shown  string
	hidden string
}

// grant mirrors model.ConsentGrant's shape and tags without importing the domain package —
// the storage seam deliberately does not depend on model, and what is under test is the
// mechanism (json names), not that one type.
type grant struct {
	Granted  bool   `json:"granted"`
	TS       string `json:"ts"`
	Version  string `json:"version"`
	Note     string `json:"-"`
	Untagged int
}

func TestStructValuedAttributesStoreUnderTheirJSONNames(t *testing.T) {
	// consent.decodeGrants documents map[string]model.ConsentGrant and
	// map[Purpose]model.ConsentGrant as shapes the consent attribute legitimately arrives
	// in, and the fake stores them without complaint — so refusing structs here meant a
	// tenant provisioner writing the documented shape passed every test and failed on
	// every production write. The stored names must be the json names, because that is
	// what consent.decodeGrant's map[string]any branch reads back (`granted`, `ts`,
	// `version`).
	type purpose string
	key := mustKey(keys.Tenant(testTenant))
	av, err := marshalItem(Item{Key: key, Attrs: map[string]any{
		"consent": map[purpose]grant{
			"corpus_retention": {Granted: true, TS: "2026-08-04T10:00:00Z", Version: "v1", Note: "dropped", Untagged: 3},
		},
	}})
	if err != nil {
		t.Fatalf("marshalItem: %v", err)
	}
	back, err := unmarshalItem(av)
	if err != nil {
		t.Fatalf("unmarshalItem: %v", err)
	}
	outer, ok := back.Attrs["consent"].(map[string]any)
	if !ok {
		t.Fatalf("consent attribute is %T, want map[string]any", back.Attrs["consent"])
	}
	inner, ok := outer["corpus_retention"].(map[string]any)
	if !ok {
		t.Fatalf("grant entry is %T, want map[string]any — consent.decodeGrant reads that shape", outer["corpus_retention"])
	}
	for name, want := range map[string]any{"granted": true, "ts": "2026-08-04T10:00:00Z", "version": "v1", "Untagged": int64(3)} {
		if got := inner[name]; got != want {
			t.Errorf("grant[%q] = %#v, want %#v", name, got, want)
		}
	}
	if _, present := inner["Note"]; present {
		t.Error(`a json:"-" field was stored`)
	}
	if len(inner) != 4 {
		t.Errorf("grant has %d fields (%v), want exactly the four stored ones", len(inner), inner)
	}
	// The fake accepts it too — that was never in doubt, and it is why the refusal was
	// invisible in tests. Asserted so the pair stays a pair.
	if err := NewMemory().Put(context.Background(), Item{Key: key, Attrs: map[string]any{"consent": map[purpose]grant{"corpus_retention": {Granted: true}}}}); err != nil {
		t.Errorf("the fake refused a struct-valued map: %v", err)
	}
}

func TestJSONFieldNameDecidesTheStoredAttributeName(t *testing.T) {
	// The stored name is what a reader looks for, so this rule is part of §6.3's attribute
	// set rather than a formatting detail: consent.decodeGrant reads `granted`, and a field
	// stored as `Granted` decodes as an absent grant, which resolves as refusal (I14) —
	// silently, because an absent purpose is a legitimate state.
	type tagged struct {
		Tagged    string `json:"tagged"`
		WithOpts  string `json:"with_opts,omitempty"`
		OptsOnly  string `json:",omitempty"`
		Untagged  string
		Skipped   string `json:"-"`
		LiteralHy string `json:"-,"`
	}
	want := map[string]string{
		"Tagged":    "tagged",
		"WithOpts":  "with_opts",
		"OptsOnly":  "OptsOnly", // no name in the tag, so the field name stands
		"Untagged":  "Untagged",
		"LiteralHy": "-", // `json:"-,"` names the field "-"; only a bare "-" skips it
	}
	rt := reflect.TypeOf(tagged{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, store := jsonFieldName(f)
		expect, shouldStore := want[f.Name]
		if store != shouldStore {
			t.Errorf("field %s: stored = %v, want %v", f.Name, store, shouldStore)
			continue
		}
		if store && name != expect {
			t.Errorf("field %s stored as %q, want %q", f.Name, name, expect)
		}
	}
}

func TestEncodedSizeCountsSetContents(t *testing.T) {
	// Nothing writes a set, but the erasure and export paths read one (see unmarshalValue),
	// and a size estimate that counted a set as a constant would be the same hole this
	// function was rewritten to close — reopened for the one record type nobody looks at.
	big := strings.Repeat("y", 1000)
	cases := map[string]types.AttributeValue{
		"string set": &types.AttributeValueMemberSS{Value: []string{big, big}},
		"number set": &types.AttributeValueMemberNS{Value: []string{big, big}},
		"binary set": &types.AttributeValueMemberBS{Value: [][]byte{[]byte(big), []byte(big)}},
	}
	for name, av := range cases {
		if got := encodedSize(av); got < 2000 {
			t.Errorf("%s measured %d bytes, want at least the 2000 bytes it holds", name, got)
		}
	}
}

func TestMarshalIsBoundedInDepth(t *testing.T) {
	key := mustKey(keys.Item(testTenant, "i_1"))

	t.Run("a self-referential map is refused rather than crashing", func(t *testing.T) {
		// Unbounded, this recurses until the stack is gone. A stack overflow kills the
		// process, so the in-flight capture dies with it and no retry can report why —
		// which is I2's "audio is never lost to a software bug" in its least recoverable
		// form. The test would not fail; the test binary would abort.
		m := map[string]any{"label": "loop"}
		m["self"] = m
		if _, err := marshalItem(Item{Key: key, Attrs: map[string]any{"x": m}}); err == nil {
			t.Error("a cyclic value was accepted")
		}
		if err := NewMemory().Put(context.Background(), Item{Key: key, Attrs: map[string]any{"x": m}}); err == nil {
			t.Error("the fake accepted a cyclic value")
		}
	})

	t.Run("nesting past DynamoDB's limit is named rather than left to the service", func(t *testing.T) {
		var v any = "leaf"
		for i := 0; i < maxNestingDepth+2; i++ {
			v = map[string]any{"n": v}
		}
		_, err := marshalItem(Item{Key: key, Attrs: map[string]any{"x": v}})
		if err == nil {
			t.Fatal("accepted a value nested past the 32-level limit; the service would answer with a ValidationException the caller cannot tell from a throttle")
		}
		if !strings.Contains(err.Error(), "nests deeper") {
			t.Errorf("error %q does not say what was wrong", err)
		}
	})

	t.Run("nesting within the limit is still accepted", func(t *testing.T) {
		var v any = "leaf"
		for i := 0; i < 20; i++ {
			v = map[string]any{"n": v}
		}
		if _, err := marshalItem(Item{Key: key, Attrs: map[string]any{"x": v}}); err != nil {
			t.Errorf("refused a legitimately nested value: %v", err)
		}
	})
}

func TestUnmarshalRefusesARecordWithoutKeyAttributes(t *testing.T) {
	// **This covers a decode-time invariant, not I16.** The attribute maps below are
	// hand-built and no DynamoDB response can contain one: PK and SK are the table's HASH
	// and RANGE keys, both declared AttributeType: S in infrastructure/template.yaml, so
	// the service cannot store an item missing either or return one from GetItem or a
	// base-table Query — and an ad-hoc `aws dynamodb put-item` must supply both too. What
	// it does establish is that every Item this package hands out carries a usable key,
	// which callers rely on when they build a follow-up write from got.Key, and that the
	// check is already correct for the day a ProjectionExpression or an index read makes it
	// reachable. Nobody assessing I16 coverage should count it.
	cases := map[string]map[string]types.AttributeValue{
		"no keys at all": {"label": &types.AttributeValueMemberS{Value: "x"}},
		"pk only":        {pkAttr: &types.AttributeValueMemberS{Value: "p"}},
		"sk only":        {skAttr: &types.AttributeValueMemberS{Value: "s"}},
		"pk not a string": {
			pkAttr: &types.AttributeValueMemberN{Value: "1"},
			skAttr: &types.AttributeValueMemberS{Value: "s"},
		},
	}
	for name, av := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalItem(av); err == nil {
				t.Error("accepted a record with no usable key")
			}
		})
	}
}

func TestSetTypesAreReadableEvenThoughNothingWritesThem(t *testing.T) {
	// The erasure path (§9.3) and the export path (§Phase 7) must read every record in
	// the table. A record they cannot decode is one they cannot erase, so an
	// unrecognised-but-representable type is decoded rather than refused.
	av := map[string]types.AttributeValue{
		pkAttr:   &types.AttributeValueMemberS{Value: "p"},
		skAttr:   &types.AttributeValueMemberS{Value: "s"},
		"tags":   &types.AttributeValueMemberSS{Value: []string{"a", "b"}},
		"counts": &types.AttributeValueMemberNS{Value: []string{"1", "2"}},
		"blobs":  &types.AttributeValueMemberBS{Value: [][]byte{{0x01}}},
	}
	back, err := unmarshalItem(av)
	if err != nil {
		t.Fatalf("unmarshalItem: %v", err)
	}
	if got, ok := back.Attrs["tags"].([]any); !ok || len(got) != 2 || got[1] != "b" {
		t.Errorf("Attrs[tags] = %#v", back.Attrs["tags"])
	}
	if got, ok := back.Attrs["counts"].([]any); !ok || got[0] != int64(1) {
		t.Errorf("Attrs[counts] = %#v", back.Attrs["counts"])
	}
	if got, ok := back.Attrs["blobs"].([]any); !ok || len(got) != 1 {
		t.Errorf("Attrs[blobs] = %#v", back.Attrs["blobs"])
	}
}

func TestOversizedItemIsRefusedByBothImplementations(t *testing.T) {
	// The ceiling is what forces the S3 overflow path for a long verbatim prompt body
	// (§3A.4) — the one content type §3A.3 says must not be truncated. Asserting both
	// implementations refuse the same item is the point: if only the fake refuses, the
	// overflow path is exercised in tests and skipped in production.
	key := mustKey(keys.Item(testTenant, "i_big"))
	item := Item{Key: key, Attrs: map[string]any{"text": strings.Repeat("x", dynamoItemLimit+1)}}

	if _, err := marshalItem(item); err == nil {
		t.Error("the DynamoDB adapter accepted an oversized item")
	}
	if err := NewMemory().Put(context.Background(), item); err == nil {
		t.Error("the fake accepted an oversized item")
	}
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	if err := d.Put(context.Background(), item); err == nil {
		t.Error("Put accepted an oversized item")
	}
	if len(stub.puts) != 0 {
		t.Error("Put sent the oversized item to DynamoDB; the pre-check exists so the failure names the S3 overflow path rather than arriving as a service validation error")
	}
}

func TestTheCeilingSeesBinaryAndNestedContent(t *testing.T) {
	// The ceiling used to be blind to every shape except a top-level string: a []byte, a
	// list, or a map counted as 8 bytes whatever it held. So all three items below passed a
	// check whose entire purpose is to force the S3 overflow path (§3A.4) for the one
	// content type §3A.3 says must not be truncated, and the write then failed at the
	// service with a ValidationException the caller cannot distinguish from a throttle.
	// A long verbatim prompt body assembled as a list of blocks is exactly the §3A.4 case.
	big := strings.Repeat("x", 600*1024)
	key := mustKey(keys.Item(testTenant, "i_big"))
	cases := map[string]map[string]any{
		"body nested in a map":   {"body": map[string]any{"text": big}},
		"blocks in a list":       {"blocks": []any{big}},
		"raw bytes":              {"audio": []byte(big)},
		"list of typed strings":  {"blocks": []string{big[:300*1024], big[:300*1024]}},
		"map nested two deep":    {"a": map[string]any{"b": map[string]any{"c": big}}},
		"many medium attributes": {"a": big[:150*1024], "b": big[:150*1024], "c": big[:150*1024]},
	}
	for name, attrs := range cases {
		t.Run(name, func(t *testing.T) {
			item := Item{Key: key, Attrs: attrs}
			stub := &stubDynamo{}
			d := newStubbed(t, stub)
			if err := d.Put(context.Background(), item); err == nil {
				t.Error("the adapter accepted an item well over the ceiling")
			} else if !strings.Contains(err.Error(), "text_key") {
				t.Errorf("error %q does not name the S3 overflow path, which is the whole reason for pre-checking rather than letting AWS refuse it", err)
			}
			if len(stub.puts) != 0 {
				t.Error("the oversized item was sent to DynamoDB anyway")
			}
			if err := NewMemory().Put(context.Background(), item); err == nil {
				t.Error("the fake accepted it, so the overflow path would be exercised in tests and skipped in production")
			}
		})
	}

	// And a small item built from the same shapes is still accepted — a ceiling that
	// refuses everything nested would push callers to flatten records to get past it.
	small := Item{Key: key, Attrs: map[string]any{
		"body":   map[string]any{"text": "short"},
		"blocks": []any{"a", "b"},
		"digest": []byte{0x01, 0x02},
	}}
	if err := NewMemory().Put(context.Background(), small); err != nil {
		t.Errorf("the fake refused a small nested item: %v", err)
	}
	if _, err := marshalItem(small); err != nil {
		t.Errorf("the adapter refused a small nested item: %v", err)
	}
}

// ---------------------------------------------------------------------------
// QueryPrefix
// ---------------------------------------------------------------------------

func page(lastKey map[string]types.AttributeValue, sks ...string) *dynamodb.QueryOutput {
	out := &dynamodb.QueryOutput{LastEvaluatedKey: lastKey}
	for _, sk := range sks {
		out.Items = append(out.Items, map[string]types.AttributeValue{
			pkAttr:        &types.AttributeValueMemberS{Value: "partition"},
			skAttr:        &types.AttributeValueMemberS{Value: sk},
			"cost_micros": &types.AttributeValueMemberN{Value: "1000"},
		})
	}
	return out
}

func cursor(sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		pkAttr: &types.AttributeValueMemberS{Value: "partition"},
		skAttr: &types.AttributeValueMemberS{Value: sk},
	}
}

func TestQueryPrefixPaginatesToExhaustion(t *testing.T) {
	// A month of usage records exceeds DynamoDB's 1MB page, and returning the first page
	// makes MonthTotal produce a figure that looks plausible and is too low — so the
	// daily spend cap in §10.5.9 stops holding.
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{
		page(cursor("a"), "a1", "a2"),
		// An empty page with a continuation key is legitimate: DynamoDB stops at the 1MB
		// boundary, not at an item boundary. Stopping here is the subtle truncation.
		page(cursor("b")),
		page(nil, "c1"),
	}}
	d := newStubbed(t, stub)

	items, err := d.QueryPrefix(context.Background(), pk, prefix, 0)
	if err != nil {
		t.Fatalf("QueryPrefix: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 across three pages", len(items))
	}
	if len(stub.queries) != 3 {
		t.Fatalf("made %d Query calls, want 3", len(stub.queries))
	}
	// The continuation key must be threaded through, or the second call re-reads the
	// first page and the loop never terminates.
	if stub.queries[0].ExclusiveStartKey != nil {
		t.Error("first page sent an ExclusiveStartKey")
	}
	for i, wantSK := range []string{"a", "b"} {
		got := stub.queries[i+1].ExclusiveStartKey
		if got == nil {
			t.Fatalf("call %d sent no ExclusiveStartKey", i+1)
		}
		s, ok := got[skAttr].(*types.AttributeValueMemberS)
		if !ok || s.Value != wantSK {
			t.Errorf("call %d ExclusiveStartKey SK = %#v, want %q", i+1, got[skAttr], wantSK)
		}
	}
	// Summing what came back is the behaviour the breaker depends on.
	var total int64
	for _, it := range items {
		if c, ok := it.Attrs["cost_micros"].(int64); ok {
			total += c
		}
	}
	if total != 3000 {
		t.Errorf("summed cost = %d, want 3000 — a short read under-counts spend", total)
	}
}

func TestQueryPrefixStopsOnceTheLimitIsReached(t *testing.T) {
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{
		page(cursor("a"), "a1", "a2", "a3"),
		page(nil, "b1"),
	}}
	d := newStubbed(t, stub)
	items, err := d.QueryPrefix(context.Background(), pk, prefix, 2)
	if err != nil {
		t.Fatalf("QueryPrefix: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if len(stub.queries) != 1 {
		t.Errorf("made %d Query calls; a satisfied limit must not pay for another page", len(stub.queries))
	}
	if stub.queries[0].Limit == nil || *stub.queries[0].Limit != 2 {
		t.Errorf("Limit = %v, want 2 so the service does not read a whole partition", stub.queries[0].Limit)
	}
}

func TestQueryPrefixLimitSurvivesAnAbsurdValue(t *testing.T) {
	// Limit is an int32 and the parameter is an int. math.MaxInt would wrap to a
	// negative Limit, which DynamoDB rejects — turning "no practical limit" into a
	// hard failure.
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{page(nil, "a1")}}
	d := newStubbed(t, stub)
	if _, err := d.QueryPrefix(context.Background(), pk, prefix, math.MaxInt); err != nil {
		t.Fatalf("QueryPrefix: %v", err)
	}
	if got := *stub.queries[0].Limit; got <= 0 {
		t.Errorf("Limit = %d; a non-positive Limit is rejected by DynamoDB", got)
	}
}

func TestQueryPrefixBuildsTheKeyCondition(t *testing.T) {
	pkVal := mustKey(keys.Tenant(testTenant)).PK
	captureSeg := mustKey(keys.Segment(testTenant, "c_1", 0)).SK

	cases := map[string]struct {
		prefix       string
		wantContains []string
		wantAbsent   []string
	}{
		"with a prefix": {
			prefix:       captureSeg[:len(captureSeg)-6],
			wantContains: []string{"#pk = :pk", "begins_with(#sk, :prefix)"},
		},
		// begins_with with an empty operand is a ValidationException, while the fake
		// treats an empty prefix as "everything in this partition". Omitting the clause
		// is what makes the two agree.
		"without a prefix": {
			prefix:       "",
			wantContains: []string{"#pk = :pk"},
			wantAbsent:   []string{"begins_with"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{page(nil)}}
			d := newStubbed(t, stub)
			if _, err := d.QueryPrefix(context.Background(), pkVal, tc.prefix, 0); err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			cond := *stub.queries[0].KeyConditionExpression
			for _, want := range tc.wantContains {
				if !strings.Contains(cond, want) {
					t.Errorf("KeyConditionExpression %q is missing %q", cond, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(cond, absent) {
					t.Errorf("KeyConditionExpression %q contains %q", cond, absent)
				}
			}
			// Sort-key order is load-bearing: keys.Segment zero-pads the sequence so
			// that lexicographic order reassembles the transcript correctly.
			if fwd := stub.queries[0].ScanIndexForward; fwd == nil || !*fwd {
				t.Error("ScanIndexForward was not set to ascending explicitly")
			}
			if cr := stub.queries[0].ConsistentRead; cr == nil || !*cr {
				t.Error("QueryPrefix did not request a consistent read")
			}
		})
	}
}

func TestQueryPrefixRefusesAnEmptyPartitionKey(t *testing.T) {
	// The only shape this method could have that reads across tenants (I11). It must
	// never be interpreted as "every partition", and it must not reach the service.
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	_, err := d.QueryPrefix(context.Background(), "", "anything", 0)
	if err == nil {
		t.Fatal("an empty partition key was accepted")
	}
	if !strings.Contains(err.Error(), "I11") {
		t.Errorf("error %q does not cite the invariant it protects", err)
	}
	if len(stub.queries) != 0 {
		t.Error("the query was sent to DynamoDB anyway")
	}
}

func TestQueryPrefixReturnsNothingRatherThanAPartialResultOnError(t *testing.T) {
	// A partial result is a wrong answer that looks like a right one, which for the
	// spend breaker means an under-count and a cap that does not hold.
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	stub := &stubDynamo{
		queryPages: []*dynamodb.QueryOutput{page(cursor("a"), "a1", "a2")},
		queryErr:   &types.ProvisionedThroughputExceededException{},
		queryErrAt: 1,
	}
	d := newStubbed(t, stub)
	items, err := d.QueryPrefix(context.Background(), pk, prefix, 0)
	if err == nil {
		t.Fatal("expected the page failure to surface")
	}
	if items != nil {
		t.Errorf("got %d items alongside the error; a truncated total is worse than no total", len(items))
	}
}

func TestQueryPrefixSurfacesAnUndecodableStoredRecord(t *testing.T) {
	// A stored attribute type this package cannot decode fails the read rather than being
	// skipped: a usage record dropped from the list is a spend total that is short by that
	// record, which the breaker trusts (§10.5.9). types.UnknownUnionMember is what the SDK
	// returns for an attribute type this SDK version does not know.
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{
		{pkAttr: &types.AttributeValueMemberS{Value: "p"}, skAttr: &types.AttributeValueMemberS{Value: "s"},
			"cost_micros": &types.UnknownUnionMember{Tag: "Q"}},
	}}}}
	d := newStubbed(t, stub)
	if _, err := d.QueryPrefix(context.Background(), pk, prefix, 0); err == nil {
		t.Error("a record this package cannot decode was returned anyway; the total would be short by that record")
	}
}

func TestOneOversizedNumberDoesNotMakeAPartitionUnreadable(t *testing.T) {
	// The reviewer's reproduction, at the granularity that mattered: the refusal was raised
	// from unmarshalItem, so a single usage record carrying a quantity of 1e19 — a legal
	// meter.Event, since Event.Quantity is a float64 bounded only from below — made every
	// MonthTotal and DayTotal for that tenant-month fail. breaker turns that into
	// ErrSpendUnknown and refuses every capture for the rest of the month, and QueryPrefix
	// could not even list the partition, so the offending key was not discoverable through
	// any sanctioned path (I16 forbids the ad-hoc Scan, and no operational script does it).
	pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
	if err != nil {
		t.Fatalf("keys.UsageMonthPrefix: %v", err)
	}
	usage := func(sk, quantity, cost string) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{
			pkAttr:        &types.AttributeValueMemberS{Value: pk},
			skAttr:        &types.AttributeValueMemberS{Value: prefix + sk},
			"quantity":    &types.AttributeValueMemberN{Value: quantity},
			"cost_micros": &types.AttributeValueMemberN{Value: cost},
		}
	}
	stub := &stubDynamo{queryPages: []*dynamodb.QueryOutput{{Items: []map[string]types.AttributeValue{
		usage("01HA", "3", "1200"),
		usage("01HB", "10000000000000000000", "2"), // the 1e19 quantity
		usage("01HC", "5", "800"),
	}}}}
	d := newStubbed(t, stub)
	items, err := d.QueryPrefix(context.Background(), pk, prefix, 0)
	if err != nil {
		t.Fatalf("QueryPrefix over a partition holding one oversized quantity: %v — the whole tenant-month is then unreadable and every capture is refused", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d records, want all 3", len(items))
	}
	// Every record's cost still reads exactly, which is what the month total is made of.
	var total int64
	for _, it := range items {
		c, ok := AsInt64(it.Attrs["cost_micros"])
		if !ok {
			t.Fatalf("cost_micros of %s is %#v and did not read as an exact integer", it.Key.SK, it.Attrs["cost_micros"])
		}
		total += c
	}
	if total != 2002 {
		t.Errorf("month total = %d micros, want 2002", total)
	}
	// The oversized value itself is readable as a float and refused as money — the
	// precision guard moved to the attribute that is money, which is where meter reads it.
	if q, ok := items[1].Attrs["quantity"].(float64); !ok || q != 1e19 {
		t.Errorf("quantity = %#v, want float64(1e19)", items[1].Attrs["quantity"])
	}
	if _, ok := AsInt64(items[1].Attrs["quantity"]); ok {
		t.Error("AsInt64 accepted a value beyond int64; money must be refused rather than rounded")
	}
}

// ---------------------------------------------------------------------------
// Key handling on the read and delete paths
// ---------------------------------------------------------------------------

func TestReadAndDeletePathsRefuseAKeyWithAnEmptyComponent(t *testing.T) {
	// The keys package cannot produce one of these, so reaching here means it was
	// bypassed. Refused rather than sent, because DynamoDB's own error for an empty key
	// attribute does not say which invariant was broken.
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	ctx := context.Background()
	for _, key := range []keys.DynamoKey{{}, {PK: "p"}, {SK: "s"}} {
		if _, err := d.Get(ctx, key); err == nil || !strings.Contains(err.Error(), "I11") {
			t.Errorf("Get(%+v) error = %v, want a refusal citing I11", key, err)
		}
		if err := d.Delete(ctx, key); err == nil || !strings.Contains(err.Error(), "I11") {
			t.Errorf("Delete(%+v) error = %v, want a refusal citing I11", key, err)
		}
	}
	if len(stub.deletes) != 0 {
		t.Error("a delete with an empty key component was sent to DynamoDB")
	}
}

func TestGetAndDeleteAddressBothKeyComponents(t *testing.T) {
	// A GetItem carrying only the partition key is a validation error, and a DeleteItem
	// carrying only the partition key would be one too — but if either were built from
	// the wrong attribute name it would address a different record.
	stub := &stubDynamo{}
	d := newStubbed(t, stub)
	key := mustKey(keys.Segment(testTenant, "c_1", 7))
	if err := d.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got := stub.deletes[0].Key
	for name, want := range map[string]string{pkAttr: key.PK, skAttr: key.SK} {
		s, ok := got[name].(*types.AttributeValueMemberS)
		if !ok || s.Value != want {
			t.Errorf("Key[%q] = %#v, want %q", name, got[name], want)
		}
	}
}

func TestDeleteOfAnAbsentItemIsNotAnError(t *testing.T) {
	// Matches both DynamoDB and the fake. The tenant-erasure path (§9.3) re-runs on
	// failure, so a second pass over already-deleted keys must not fail.
	d := newStubbed(t, &stubDynamo{})
	if err := d.Delete(context.Background(), mustKey(keys.Capture(testTenant, "c_gone"))); err != nil {
		t.Errorf("Delete of an absent item returned %v", err)
	}
}

// ---------------------------------------------------------------------------
// Conformance: the adapter and the fake must answer the same questions the same way
// ---------------------------------------------------------------------------

// storeDynamo is a minimal in-memory DynamoDB: enough of GetItem, PutItem, DeleteItem,
// and Query to run the same behavioural table against the adapter and against the fake.
//
// It exists because "the two implementations agree" is the property this file is really
// about, and no amount of canned-response testing establishes it. Deliberately paginates
// in two-item pages so every conformance query crosses a page boundary — the real 1MB
// boundary cannot be reached in a unit test, and a pagination bug that only appears
// beyond one page would otherwise go unseen until a month of usage records existed.
type storeDynamo struct {
	items map[string]map[string]types.AttributeValue
	order []string
}

func newStoreDynamo() *storeDynamo {
	return &storeDynamo{items: map[string]map[string]types.AttributeValue{}}
}

func (s *storeDynamo) str(av map[string]types.AttributeValue, name string) string {
	if v, ok := av[name].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func (s *storeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	it := s.items[s.str(in.Key, pkAttr)+"\x00"+s.str(in.Key, skAttr)]
	return &dynamodb.GetItemOutput{Item: it}, nil
}

func (s *storeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	ck := s.str(in.Item, pkAttr) + "\x00" + s.str(in.Item, skAttr)
	if in.ConditionExpression != nil {
		if _, exists := s.items[ck]; exists {
			return nil, &types.ConditionalCheckFailedException{}
		}
	}
	if _, exists := s.items[ck]; !exists {
		s.order = append(s.order, ck)
	}
	s.items[ck] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (s *storeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	delete(s.items, s.str(in.Key, pkAttr)+"\x00"+s.str(in.Key, skAttr))
	return &dynamodb.DeleteItemOutput{}, nil
}

func (s *storeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	wantPK := s.str(in.ExpressionAttributeValues, ":pk")
	wantPrefix := s.str(in.ExpressionAttributeValues, ":prefix")

	// Collect matches in sort-key order, which is the order DynamoDB returns.
	var sks []string
	byKey := map[string]map[string]types.AttributeValue{}
	for _, ck := range s.order {
		it, ok := s.items[ck]
		if !ok || s.str(it, pkAttr) != wantPK {
			continue
		}
		sk := s.str(it, skAttr)
		if wantPrefix != "" && !strings.HasPrefix(sk, wantPrefix) {
			continue
		}
		sks = append(sks, sk)
		byKey[sk] = it
	}
	sortStrings(sks)

	start := 0
	if in.ExclusiveStartKey != nil {
		after := s.str(in.ExclusiveStartKey, skAttr)
		for i, sk := range sks {
			if sk > after {
				start = i
				break
			}
			start = i + 1
		}
	}
	const pageSize = 2
	end := start + pageSize
	if in.Limit != nil && int(*in.Limit) < pageSize {
		end = start + int(*in.Limit)
	}
	if end > len(sks) {
		end = len(sks)
	}
	out := &dynamodb.QueryOutput{}
	for _, sk := range sks[start:end] {
		out.Items = append(out.Items, byKey[sk])
	}
	if end < len(sks) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{
			pkAttr: &types.AttributeValueMemberS{Value: wantPK},
			skAttr: &types.AttributeValueMemberS{Value: sks[end-1]},
		}
	}
	return out, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestAdapterAndFakeAgree(t *testing.T) {
	// Both implementations, one behavioural table. A case that passes here and fails in
	// production would have to be something neither the fake nor storeDynamo models —
	// which is exactly the list the package report calls out as unverified.
	implementations := map[string]func(t *testing.T) Repository{
		"fake": func(*testing.T) Repository { return NewMemory() },
		"dynamodb adapter": func(t *testing.T) Repository {
			d, err := NewDynamo(newStoreDynamo(), "voicenotes-test")
			if err != nil {
				t.Fatalf("NewDynamo: %v", err)
			}
			return d
		},
	}

	cases := map[string]func(t *testing.T, r Repository){
		"get of a missing item is ErrNotFound": func(t *testing.T, r Repository) {
			_, err := r.Get(context.Background(), mustKey(keys.Capture(testTenant, "c_absent")))
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		},
		"put then get returns the attributes": func(t *testing.T, r Repository) {
			key := mustKey(keys.Capture(testTenant, "c_1"))
			in := Item{Key: key, Attrs: map[string]any{"label": "compression", "n": int64(4)}}
			if err := r.Put(context.Background(), in); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := r.Get(context.Background(), key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Attrs["label"] != "compression" || got.Attrs["n"] != int64(4) {
				t.Errorf("Attrs = %#v", got.Attrs)
			}
			if got.Key != key {
				t.Errorf("Key = %+v, want %+v", got.Key, key)
			}
		},
		"put replaces": func(t *testing.T, r Repository) {
			key := mustKey(keys.Capture(testTenant, "c_1"))
			ctx := context.Background()
			if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"label": "first"}}); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"label": "second"}}); err != nil {
				t.Fatalf("second Put: %v", err)
			}
			got, err := r.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Attrs["label"] != "second" {
				t.Errorf("label = %v, want the replacement", got.Attrs["label"])
			}
		},
		"put-once refuses the second write": func(t *testing.T, r Repository) {
			key := mustKey(keys.Audit(testTenant, "01HAUDIT"))
			ctx := context.Background()
			item := Item{Key: key, Attrs: map[string]any{"action": "read"}}
			if err := r.PutOnce(ctx, item); err != nil {
				t.Fatalf("first PutOnce: %v", err)
			}
			err := r.PutOnce(ctx, item)
			if !errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("second PutOnce err = %v, want ErrAlreadyExists", err)
			}
			// And the first record must be intact: a write-once record that a failed
			// second attempt corrupted would break I13's "never mutated".
			got, err := r.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Attrs["action"] != "read" {
				t.Errorf("Attrs = %#v, want the original record", got.Attrs)
			}
		},
		"put-once still allows a different key": func(t *testing.T, r Repository) {
			ctx := context.Background()
			for _, id := range []string{"01HA", "01HB"} {
				if err := r.PutOnce(ctx, Item{Key: mustKey(keys.Audit(testTenant, id)), Attrs: map[string]any{"a": "b"}}); err != nil {
					t.Fatalf("PutOnce(%s): %v", id, err)
				}
			}
		},
		"delete of an absent key is not an error": func(t *testing.T, r Repository) {
			if err := r.Delete(context.Background(), mustKey(keys.Capture(testTenant, "c_absent"))); err != nil {
				t.Errorf("Delete: %v", err)
			}
		},
		"delete then get is ErrNotFound": func(t *testing.T, r Repository) {
			key := mustKey(keys.Capture(testTenant, "c_1"))
			ctx := context.Background()
			if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"label": "x"}}); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := r.Delete(ctx, key); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := r.Get(ctx, key); !errors.Is(err, ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound", err)
			}
		},
		"query returns sort-key order across page boundaries": func(t *testing.T, r Repository) {
			ctx := context.Background()
			// Written out of order and with a two-digit gap, so a lexicographic sort of
			// unpadded sequences would put 10 before 2. keys.Segment's zero padding is
			// what prevents it, and this asserts the ordering both back ends produce.
			for _, seq := range []int{10, 2, 0, 7, 1, 9, 3} {
				key := mustKey(keys.Segment(testTenant, "c_1", seq))
				if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"seq": int64(seq)}}); err != nil {
					t.Fatalf("Put(seq=%d): %v", seq, err)
				}
			}
			pk := mustKey(keys.Tenant(testTenant)).PK
			prefix := mustKey(keys.Segment(testTenant, "c_1", 0)).SK
			prefix = prefix[:len(prefix)-6]

			items, err := r.QueryPrefix(ctx, pk, prefix, 0)
			if err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			want := []int64{0, 1, 2, 3, 7, 9, 10}
			if len(items) != len(want) {
				t.Fatalf("got %d items, want %d — a short read is how pagination bugs present", len(items), len(want))
			}
			for i, w := range want {
				if items[i].Attrs["seq"] != w {
					t.Errorf("item %d seq = %v, want %d", i, items[i].Attrs["seq"], w)
				}
			}
		},
		"query filters by prefix within one partition": func(t *testing.T, r Repository) {
			ctx := context.Background()
			for _, k := range []keys.DynamoKey{
				mustKey(keys.Segment(testTenant, "c_1", 0)),
				mustKey(keys.Segment(testTenant, "c_2", 0)),
				mustKey(keys.Capture(testTenant, "c_1")),
				mustKey(keys.Audit(testTenant, "01HA")),
			} {
				if err := r.Put(ctx, Item{Key: k, Attrs: map[string]any{"x": "y"}}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			pk := mustKey(keys.Tenant(testTenant)).PK
			prefix := mustKey(keys.Segment(testTenant, "c_1", 0)).SK
			prefix = prefix[:len(prefix)-6]
			items, err := r.QueryPrefix(ctx, pk, prefix, 0)
			if err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("got %d items, want only c_1's segment: %+v", len(items), items)
			}
		},
		"query honours a limit": func(t *testing.T, r Repository) {
			ctx := context.Background()
			for seq := 0; seq < 7; seq++ {
				if err := r.Put(ctx, Item{Key: mustKey(keys.Segment(testTenant, "c_1", seq)), Attrs: map[string]any{"x": "y"}}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			pk := mustKey(keys.Tenant(testTenant)).PK
			prefix := mustKey(keys.Segment(testTenant, "c_1", 0)).SK
			prefix = prefix[:len(prefix)-6]
			for _, limit := range []int{1, 3, 7, 100} {
				items, err := r.QueryPrefix(ctx, pk, prefix, limit)
				if err != nil {
					t.Fatalf("QueryPrefix(limit=%d): %v", limit, err)
				}
				want := limit
				if want > 7 {
					want = 7
				}
				if len(items) != want {
					t.Errorf("limit %d returned %d items, want %d", limit, len(items), want)
				}
			}
		},
		"query with no prefix returns the whole partition": func(t *testing.T, r Repository) {
			ctx := context.Background()
			for _, k := range []keys.DynamoKey{
				mustKey(keys.Capture(testTenant, "c_1")),
				mustKey(keys.Item(testTenant, "i_1")),
				mustKey(keys.Thread(testTenant, "th_1")),
			} {
				if err := r.Put(ctx, Item{Key: k, Attrs: map[string]any{"x": "y"}}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			pk := mustKey(keys.Tenant(testTenant)).PK
			items, err := r.QueryPrefix(ctx, pk, "", 0)
			if err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			if len(items) != 3 {
				t.Errorf("got %d items, want 3", len(items))
			}
		},
		"query of an unrelated partition returns nothing": func(t *testing.T, r Repository) {
			ctx := context.Background()
			if err := r.Put(ctx, Item{Key: mustKey(keys.Capture(testTenant, "c_1")), Attrs: map[string]any{"x": "y"}}); err != nil {
				t.Fatalf("Put: %v", err)
			}
			// A different tenant's partition. The cross-tenant read must come back empty
			// rather than erroring, because §9.1 requires a 404 and not a 403 — a 403
			// would confirm the resource exists.
			other := mustKey(keys.Tenant(keys.TenantID("t_other"))).PK
			items, err := r.QueryPrefix(ctx, other, "", 0)
			if err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			if len(items) != 0 {
				t.Errorf("got %d items from another tenant's partition (I11)", len(items))
			}
		},
		"an empty key component is refused on write": func(t *testing.T, r Repository) {
			ctx := context.Background()
			for _, key := range []keys.DynamoKey{{}, {PK: "p"}, {SK: "s"}} {
				if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"x": "y"}}); err == nil {
					t.Errorf("Put(%+v) was accepted; the keys package cannot produce this (I11)", key)
				}
				if err := r.PutOnce(ctx, Item{Key: key, Attrs: map[string]any{"x": "y"}}); err == nil {
					t.Errorf("PutOnce(%+v) was accepted (I11)", key)
				}
			}
		},
		"a TTL and GSI1 pair survive a round trip": func(t *testing.T, r Repository) {
			ctx := context.Background()
			key := mustKey(keys.Capture(testTenant, "c_1"))
			gsiPK, gsiSK, err := keys.GSI1(testTenant, "2026-08-04T10:00:00Z")
			if err != nil {
				t.Fatalf("keys.GSI1: %v", err)
			}
			in := Item{Key: key, GSI1PK: gsiPK, GSI1SK: gsiSK, TTL: 1_800_000_000, Attrs: map[string]any{"label": "x"}}
			if err := r.Put(ctx, in); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := r.Get(ctx, key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.GSI1PK != gsiPK || got.GSI1SK != gsiSK || got.TTL != in.TTL {
				t.Errorf("got GSI1=(%q,%q) TTL=%d, want (%q,%q) %d", got.GSI1PK, got.GSI1SK, got.TTL, gsiPK, gsiSK, in.TTL)
			}
		},
		"a record with no attributes reads back with a nil Attrs map": func(t *testing.T, r Repository) {
			// The adapter used to allocate Attrs unconditionally, so this returned a non-nil
			// empty map in production and nil from the fake. A caller written as
			// `if item.Attrs == nil { … }` — consent.decodeGrants and kmsref.Resolve both
			// branch on it — would take the opposite branch from the one every test takes.
			// DynamoDB stores no attribute for an empty map and cannot tell it from an
			// absent one, so nil is the only answer both implementations can give.
			ctx := context.Background()
			for name, in := range map[string]map[string]any{"nil": nil, "empty": {}} {
				key := mustKey(keys.Capture(testTenant, "c_"+name))
				if err := r.Put(ctx, Item{Key: key, Attrs: in}); err != nil {
					t.Fatalf("Put(%s): %v", name, err)
				}
				got, err := r.Get(ctx, key)
				if err != nil {
					t.Fatalf("Get(%s): %v", name, err)
				}
				if got.Attrs != nil {
					t.Errorf("Attrs written as %s came back as %#v, want nil", name, got.Attrs)
				}
			}
		},
		"a value that cannot be stored faithfully is refused by both": func(t *testing.T, r Repository) {
			// The pair that matters: a write that succeeds against the fake and fails
			// against DynamoDB is a bug no test can see. uint64 above int64 cannot survive
			// the round trip, so both refuse it; the struct-valued map is the shape
			// consent.decodeGrants documents, so both accept it.
			ctx := context.Background()
			key := mustKey(keys.Tenant(testTenant))
			if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{"h": uint64(math.MaxUint64)}}); err == nil {
				t.Error("a uint64 beyond int64 was accepted; it reads back as a value nobody wrote")
			}
			if err := r.Put(ctx, Item{Key: key, Attrs: map[string]any{
				"consent": map[string]grant{"corpus_retention": {Granted: true, TS: "2026-08-04T10:00:00Z", Version: "v1"}},
			}}); err != nil {
				t.Errorf("a struct-valued map was refused: %v — consent.decodeGrants documents this shape as storable", err)
			}
		},
		"money survives a write and a query": func(t *testing.T, r Repository) {
			ctx := context.Background()
			// Deliberately a value float64 cannot hold, written through the same shape
			// meter.Record uses, and read back the way MonthTotal reads it.
			const cost = int64(1)<<53 + 1
			key := mustKey(keys.Usage(testTenant, "2026-08", "stt_seconds", "01HUSAGE"))
			ttl := int64(1_800_000_000)
			if err := r.PutOnce(ctx, Item{
				Key:   key,
				Attrs: map[string]any{"cost_micros": cost, "ts": "2026-08-04T10:00:00Z", "ttl": ttl},
				TTL:   ttl,
			}); err != nil {
				t.Fatalf("PutOnce: %v", err)
			}
			pk, prefix, err := keys.UsageMonthPrefix(testTenant, "2026-08")
			if err != nil {
				t.Fatalf("keys.UsageMonthPrefix: %v", err)
			}
			items, err := r.QueryPrefix(ctx, pk, prefix, 0)
			if err != nil {
				t.Fatalf("QueryPrefix: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("got %d items, want 1", len(items))
			}
			got, ok := items[0].Attrs["cost_micros"].(int64)
			if !ok {
				t.Fatalf("cost_micros is %T, want int64 — meter.MonthTotal asserts int64 directly", items[0].Attrs["cost_micros"])
			}
			if got != cost {
				t.Errorf("cost_micros = %d, want %d", got, cost)
			}
		},
	}

	for implName, build := range implementations {
		for caseName, run := range cases {
			t.Run(implName+"/"+caseName, func(t *testing.T) {
				run(t, build(t))
			})
		}
	}
}
