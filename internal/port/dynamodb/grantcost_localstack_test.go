package dynamodb_test

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"

	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
	"github.com/truvity/sluis/internal/port/porttest/grantcost"
)

// countingAPI passes every request to the real client and counts it by
// operation: what the table is billed for.
type countingAPI struct {
	dynamoport.API
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingAPI) count(op string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[op]++
}

func (c *countingAPI) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.calls)
}

func (c *countingAPI) GetItem(ctx context.Context, in *ddb.GetItemInput, opts ...func(*ddb.Options)) (*ddb.GetItemOutput, error) {
	c.count("GetItem")
	return c.API.GetItem(ctx, in, opts...)
}

func (c *countingAPI) PutItem(ctx context.Context, in *ddb.PutItemInput, opts ...func(*ddb.Options)) (*ddb.PutItemOutput, error) {
	c.count("PutItem")
	return c.API.PutItem(ctx, in, opts...)
}

func (c *countingAPI) DeleteItem(ctx context.Context, in *ddb.DeleteItemInput, opts ...func(*ddb.Options)) (*ddb.DeleteItemOutput, error) {
	c.count("DeleteItem")
	return c.API.DeleteItem(ctx, in, opts...)
}

func (c *countingAPI) Query(ctx context.Context, in *ddb.QueryInput, opts ...func(*ddb.Options)) (*ddb.QueryOutput, error) {
	c.count("Query")
	return c.API.Query(ctx, in, opts...)
}

func (c *countingAPI) Scan(ctx context.Context, in *ddb.ScanInput, opts ...func(*ddb.Options)) (*ddb.ScanOutput, error) {
	c.count("Scan")
	return c.API.Scan(ctx, in, opts...)
}

// What one grant costs a real DynamoDB API (LocalStack), held to the same
// grantcost.Budgets as the fake and the in-memory adapter. It runs where
// ACCESS_ROSTER_DYNAMODB_URL is set -- `just test-s3`, and CI's LocalStack
// job, whose -run pattern takes every test named for DynamoDB -- and skips
// elsewhere, as the conformance suite does.
func TestGrantCostOnDynamoDB(t *testing.T) {
	url := localstack(t)
	grantcost.Run(t, func(t *testing.T) grantcost.Env {
		ctx := context.Background()
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		client := ddb.NewFromConfig(awsCfg, func(o *ddb.Options) { o.BaseEndpoint = aws.String(url) })
		api := &countingAPI{API: client, calls: map[string]int{}}
		name := "ar-" + randomName(t)
		s, err := dynamoport.New(ctx, api, dynamoport.Config{Table: name, Endpoint: url, Create: true},
			dynamoport.WithPollInterval(100*time.Millisecond))
		if err != nil {
			t.Fatalf("opening the table: %v", err)
		}
		t.Cleanup(func() {
			_, _ = client.DeleteTable(context.Background(), &ddb.DeleteTableInput{TableName: aws.String(name)})
		})
		return grantcost.Env{Set: s.Set(), Advance: s.Advance, Calls: api.snapshot}
	})
}
