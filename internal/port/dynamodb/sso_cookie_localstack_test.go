package dynamodb_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"

	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
)

// The browser sign-in over a real DynamoDB API (LocalStack): the cookie's
// pointer is written, resolved, ended and expired there as over the fake. It
// runs where ACCESS_ROSTER_DYNAMODB_URL is set -- `just test-s3` -- and skips
// elsewhere.
func TestSSOCookieOnDynamoDB(t *testing.T) {
	url := localstack(t)
	ctx := context.Background()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	client := ddb.NewFromConfig(awsCfg, func(o *ddb.Options) { o.BaseEndpoint = aws.String(url) })
	name := "ar-" + randomName(t)

	s, err := dynamoport.New(ctx, client, dynamoport.Config{Table: name, Endpoint: url, Create: true},
		dynamoport.WithPollInterval(100*time.Millisecond))
	if err != nil {
		t.Fatalf("opening the table: %v", err)
	}

	t.Cleanup(func() {
		_, _ = client.DeleteTable(context.Background(), &ddb.DeleteTableInput{TableName: aws.String(name)})
	})

	dynamoport.SSOCookieFlow(t, s.Set(), s.Advance)
}
