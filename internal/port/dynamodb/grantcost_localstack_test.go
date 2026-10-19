package dynamodb_test

import (
	"context"
	"maps"
	"sync"
	"testing"

	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/truvity/sluis/internal/port"
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

// What a reuse of a spent refresh token costs a real DynamoDB API
// (LocalStack): the first presentation after the grace window ends the
// session, a repeat finds it gone. Runs where ACCESS_ROSTER_DYNAMODB_URL is
// set, as TestGrantCostOnDynamoDB does, and skips elsewhere.
func TestReuseCostOnDynamoDB(t *testing.T) {
	url := localstack(t)
	api := &countingAPI{calls: map[string]int{}}
	tt := tables(t, url, api)
	r := tt.Router()
	dynamoport.ReuseCost(t, grantcost.Env{Set: port.Set{State: r, Index: r}, Advance: tt.Advance, Calls: api.snapshot})
}
