package dynamofake

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var table = "chintan-test"

// TestTheFakeRefusesTheShapesTheServiceRefuses is the check on the check. A
// validator nobody has watched reject anything is a validator nobody should
// trust — and this one exists precisely because the previous fake accepted
// everything.
func TestTheFakeRefusesTheShapesTheServiceRefuses(t *testing.T) {
	bad := map[string]types.AttributeValue{
		"a nil binary":                 &types.AttributeValueMemberB{Value: nil},
		"an empty binary":              &types.AttributeValueMemberB{Value: []byte{}},
		"a nil attribute":              nil,
		"an empty number":              &types.AttributeValueMemberN{Value: ""},
		"an empty string set":          &types.AttributeValueMemberSS{Value: []string{}},
		"a nil binary inside a list":   &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberB{}}},
		"a nil binary inside a map":    &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"x": &types.AttributeValueMemberB{}}},
		"a member type nobody defined": &types.UnknownUnionMember{Tag: "Q"},
	}

	for name, value := range bad {
		t.Run(name, func(t *testing.T) {
			api := New()
			_, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
				TableName: &table,
				Item: map[string]types.AttributeValue{
					"pk":    &types.AttributeValueMemberS{Value: "USER#tenant-a"},
					"sk":    &types.AttributeValueMemberS{Value: "THING#1"},
					"thing": value,
				},
			})
			if err == nil {
				t.Fatal("the fake accepted an attribute DynamoDB refuses")
			}
			if !strings.Contains(err.Error(), "ValidationException") {
				t.Errorf("error = %v, want a ValidationException", err)
			}
		})
	}

	t.Run("an empty key attribute", func(t *testing.T) {
		api := New()
		_, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
			TableName: &table,
			Item: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: ""},
				"sk": &types.AttributeValueMemberS{Value: "THING#1"},
			},
		})
		if err == nil {
			t.Fatal("the fake accepted an empty partition key")
		}
	})

	// The legal shapes must still be legal, or the validator would fail every
	// write and prove nothing.
	t.Run("shapes the service accepts", func(t *testing.T) {
		api := New()
		if _, err := api.PutItem(context.Background(), &dynamodb.PutItemInput{
			TableName: &table,
			Item: map[string]types.AttributeValue{
				"pk":            &types.AttributeValueMemberS{Value: "USER#tenant-a"},
				"sk":            &types.AttributeValueMemberS{Value: "THING#1"},
				"an empty text": &types.AttributeValueMemberS{Value: ""},
				"an empty list": &types.AttributeValueMemberL{Value: nil},
				"an empty map":  &types.AttributeValueMemberM{Value: nil},
				"a null":        &types.AttributeValueMemberNULL{Value: true},
				"a bool":        &types.AttributeValueMemberBOOL{Value: false},
				"some bytes":    &types.AttributeValueMemberB{Value: []byte{0}},
			},
		}); err != nil {
			t.Fatalf("the fake refused a legal item: %v", err)
		}
	})
}
