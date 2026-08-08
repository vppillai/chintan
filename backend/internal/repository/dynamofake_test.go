package repository_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// fakeDynamo is a small in-process DynamoDB good enough to exercise the parts
// of the API DynamoStore actually depends on: partition/sort key ordering,
// ExclusiveStartKey and Limit, LastEvaluatedKey, FilterExpression,
// ProjectionExpression, ScanIndexForward, GSI1, and conditional writes.
//
// It evaluates condition and filter expressions for real rather than pattern
// matching on the expression string, so a store that emits the wrong condition
// fails here instead of passing and then losing data in production.
type fakeDynamo struct {
	mu sync.Mutex
	// items keyed by pk then sk
	items map[string]map[string]map[string]types.AttributeValue

	// pageSize, when non-zero, caps how many items one Query returns
	// regardless of Limit, standing in for the 1MB response cap. It is how the
	// tests prove the store follows LastEvaluatedKey.
	pageSize int

	// gsi1Projected is GSI1's INCLUDE projection, read from the CloudFormation
	// template. An index query returns only these attributes plus the key
	// attributes, so a store that reads something the template does not project
	// fails here rather than in production, where widening the projection means
	// deleting and rebuilding the index.
	gsi1Projected map[string]bool

	queries []*dynamodb.QueryInput
	gets    int
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{
		items:         make(map[string]map[string]map[string]types.AttributeValue),
		gsi1Projected: gsi1NonKeyAttributes(),
	}
}

// gsi1NonKeyAttributes parses the NonKeyAttributes of the gsi1 projection out of
// infrastructure/template.yaml, so the tests are pinned to the index that will
// actually be deployed rather than to a copy that can drift away from it.
func gsi1NonKeyAttributes() map[string]bool {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return nil // template unavailable; projection is not enforced
	}
	out := map[string]bool{}
	inProjection := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "NonKeyAttributes:" {
			inProjection = true
			continue
		}
		if !inProjection {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			break
		}
		out[strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))] = true
	}
	return out
}

const templatePath = "../../../infrastructure/template.yaml"

func (f *fakeDynamo) put(item map[string]types.AttributeValue) {
	pk := av(item["pk"])
	sk := av(item["sk"])
	if f.items[pk] == nil {
		f.items[pk] = make(map[string]map[string]types.AttributeValue)
	}
	f.items[pk][sk] = item
}

func av(v types.AttributeValue) string {
	switch t := v.(type) {
	case *types.AttributeValueMemberS:
		return t.Value
	case *types.AttributeValueMemberN:
		return t.Value
	}
	return ""
}

func (f *fakeDynamo) GetItem(ctx context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	item := f.items[av(in.Key["pk"])][av(in.Key["sk"])]
	if item == nil {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: cloneItem(item)}, nil
}

func (f *fakeDynamo) PutItem(ctx context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := av(in.Item["pk"]), av(in.Item["sk"])
	existing := f.items[pk][sk]
	if in.ConditionExpression != nil {
		ok, err := evalExpr(*in.ConditionExpression, existing, in.ExpressionAttributeValues)
		if err != nil {
			return nil, err
		}
		if !ok {
			out := &types.ConditionalCheckFailedException{Message: aws.String("condition failed")}
			if in.ReturnValuesOnConditionCheckFailure == types.ReturnValuesOnConditionCheckFailureAllOld && existing != nil {
				out.Item = cloneItem(existing)
			}
			return nil, out
		}
	}
	f.put(cloneItem(in.Item))
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := av(in.Key["pk"]), av(in.Key["sk"])
	existing := f.items[pk][sk]
	if in.ConditionExpression != nil {
		ok, err := evalExpr(*in.ConditionExpression, existing, in.ExpressionAttributeValues)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &types.ConditionalCheckFailedException{Message: aws.String("condition failed")}
		}
	}
	updated := cloneItem(existing)
	if updated == nil {
		updated = map[string]types.AttributeValue{"pk": in.Key["pk"], "sk": in.Key["sk"]}
	}
	// Only "SET a = :x, b = :y" is used by the store.
	expr := strings.TrimSpace(aws.ToString(in.UpdateExpression))
	expr = strings.TrimPrefix(expr, "SET ")
	for _, assign := range strings.Split(expr, ",") {
		name, valRef, ok := strings.Cut(assign, "=")
		if !ok {
			return nil, fmt.Errorf("fakeDynamo: unsupported update %q", assign)
		}
		v, ok := in.ExpressionAttributeValues[strings.TrimSpace(valRef)]
		if !ok {
			return nil, fmt.Errorf("fakeDynamo: unknown value %q", valRef)
		}
		updated[strings.TrimSpace(name)] = v
	}
	f.put(updated)
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *fakeDynamo) DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := av(in.Key["pk"]), av(in.Key["sk"])
	existing := f.items[pk][sk]
	if in.ConditionExpression != nil {
		ok, err := evalExpr(*in.ConditionExpression, existing, in.ExpressionAttributeValues)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &types.ConditionalCheckFailedException{Message: aws.String("condition failed")}
		}
	}
	delete(f.items[pk], sk)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) Query(ctx context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, in)

	pkAttr, skAttr := "pk", "sk"
	if in.IndexName != nil {
		pkAttr, skAttr = "gsi1pk", "gsi1sk"
	}

	wantPK := av(in.ExpressionAttributeValues[":pk"])
	prefix := av(in.ExpressionAttributeValues[":sk_prefix"])

	// Gather candidates across the whole table; the index is logical here.
	var candidates []map[string]types.AttributeValue
	for _, partition := range f.items {
		for _, item := range partition {
			if av(item[pkAttr]) != wantPK {
				continue
			}
			if prefix != "" && !strings.HasPrefix(av(item[skAttr]), prefix) {
				continue
			}
			candidates = append(candidates, item)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return av(candidates[i][skAttr]) < av(candidates[j][skAttr])
	})
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
			candidates[i], candidates[j] = candidates[j], candidates[i]
		}
	}

	if len(in.ExclusiveStartKey) > 0 {
		startSK := av(in.ExclusiveStartKey[skAttr])
		for i, item := range candidates {
			if av(item[skAttr]) == startSK {
				candidates = candidates[i+1:]
				break
			}
		}
	}

	// Limit bounds items *evaluated*, before the filter, exactly like DynamoDB.
	evaluate := len(candidates)
	if in.Limit != nil && int(*in.Limit) < evaluate {
		evaluate = int(*in.Limit)
	}
	if f.pageSize > 0 && f.pageSize < evaluate {
		evaluate = f.pageSize
	}

	out := &dynamodb.QueryOutput{}
	for _, item := range candidates[:evaluate] {
		if in.FilterExpression != nil {
			ok, err := evalExpr(*in.FilterExpression, item, in.ExpressionAttributeValues)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if in.IndexName != nil {
			item = applyIndexProjection(item, f.gsi1Projected)
		}
		out.Items = append(out.Items, project(item, in.ProjectionExpression, pkAttr, skAttr))
	}
	out.Count = int32(len(out.Items))

	if evaluate < len(candidates) {
		last := candidates[evaluate-1]
		key := map[string]types.AttributeValue{
			"pk": last["pk"],
			"sk": last["sk"],
		}
		if in.IndexName != nil {
			key["gsi1pk"] = last["gsi1pk"]
			key["gsi1sk"] = last["gsi1sk"]
		}
		out.LastEvaluatedKey = key
	}
	return out, nil
}

// applyIndexProjection drops everything the GSI does not carry. DynamoDB does
// this silently, which is why a missing attribute shows up as an empty field
// rather than an error.
func applyIndexProjection(item map[string]types.AttributeValue, projected map[string]bool) map[string]types.AttributeValue {
	if projected == nil {
		return item
	}
	// Table and index key attributes are always projected.
	always := map[string]bool{"pk": true, "sk": true, "gsi1pk": true, "gsi1sk": true}
	out := make(map[string]types.AttributeValue, len(item))
	for k, v := range item {
		if always[k] || projected[k] {
			out[k] = v
		}
	}
	return out
}

func project(item map[string]types.AttributeValue, projection *string, pkAttr, skAttr string) map[string]types.AttributeValue {
	if projection == nil {
		return cloneItem(item)
	}
	keep := map[string]bool{pkAttr: true, skAttr: true}
	for _, name := range strings.Split(*projection, ",") {
		keep[strings.TrimSpace(name)] = true
	}
	out := make(map[string]types.AttributeValue, len(keep))
	for k, v := range item {
		if keep[k] {
			out[k] = v
		}
	}
	return out
}

func cloneItem(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	if item == nil {
		return nil
	}
	out := make(map[string]types.AttributeValue, len(item))
	for k, v := range item {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------- expressions

// evalExpr evaluates the subset of DynamoDB condition/filter syntax the store
// emits: AND, OR, parentheses, attribute_exists, attribute_not_exists,
// begins_with, and the comparisons = <> < >.
func evalExpr(expr string, item map[string]types.AttributeValue, values map[string]types.AttributeValue) (bool, error) {
	p := &exprParser{tokens: tokenizeExpr(expr), item: item, values: values}
	got, err := p.parseOr()
	if err != nil {
		return false, err
	}
	if p.pos != len(p.tokens) {
		return false, fmt.Errorf("fakeDynamo: trailing tokens in %q", expr)
	}
	return got, nil
}

func tokenizeExpr(expr string) []string {
	var tokens []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range expr {
		switch r {
		case '(', ')', ',':
			flush()
			tokens = append(tokens, string(r))
		case ' ', '\t', '\n':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

type exprParser struct {
	tokens []string
	pos    int
	item   map[string]types.AttributeValue
	values map[string]types.AttributeValue
}

func (p *exprParser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return ""
}

func (p *exprParser) next() string {
	t := p.peek()
	p.pos++
	return t
}

func (p *exprParser) parseOr() (bool, error) {
	left, err := p.parseAnd()
	if err != nil {
		return false, err
	}
	for strings.EqualFold(p.peek(), "OR") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return false, err
		}
		left = left || right
	}
	return left, nil
}

func (p *exprParser) parseAnd() (bool, error) {
	left, err := p.parseTerm()
	if err != nil {
		return false, err
	}
	for strings.EqualFold(p.peek(), "AND") {
		p.next()
		right, err := p.parseTerm()
		if err != nil {
			return false, err
		}
		left = left && right
	}
	return left, nil
}

func (p *exprParser) parseTerm() (bool, error) {
	if p.peek() == "(" {
		p.next()
		got, err := p.parseOr()
		if err != nil {
			return false, err
		}
		if p.next() != ")" {
			return false, fmt.Errorf("fakeDynamo: unbalanced parentheses")
		}
		return got, nil
	}

	tok := p.next()
	switch tok {
	case "attribute_exists", "attribute_not_exists", "begins_with":
		if p.next() != "(" {
			return false, fmt.Errorf("fakeDynamo: %s expects (", tok)
		}
		name := p.next()
		var arg string
		if p.peek() == "," {
			p.next()
			arg = p.next()
		}
		if p.next() != ")" {
			return false, fmt.Errorf("fakeDynamo: %s expects )", tok)
		}
		_, present := p.item[name]
		switch tok {
		case "attribute_exists":
			return present, nil
		case "attribute_not_exists":
			return !present, nil
		default:
			return strings.HasPrefix(av(p.item[name]), av(p.values[arg])), nil
		}
	}

	op := p.next()
	valRef := p.next()
	want, ok := p.values[valRef]
	if !ok {
		return false, fmt.Errorf("fakeDynamo: unknown value %q", valRef)
	}
	have, present := p.item[tok]
	if !present {
		// A missing attribute never satisfies a comparison.
		return false, nil
	}
	switch op {
	case "=":
		return av(have) == av(want), nil
	case "<>":
		return av(have) != av(want), nil
	case "<", ">":
		l, err1 := strconv.ParseFloat(av(have), 64)
		r, err2 := strconv.ParseFloat(av(want), 64)
		if err1 != nil || err2 != nil {
			if op == "<" {
				return av(have) < av(want), nil
			}
			return av(have) > av(want), nil
		}
		if op == "<" {
			return l < r, nil
		}
		return l > r, nil
	}
	return false, fmt.Errorf("fakeDynamo: unsupported operator %q", op)
}
