package dynamodb_test

import (
	"testing"

	"github.com/truvity/sluis/internal/port"
	dynamoport "github.com/truvity/sluis/internal/port/dynamodb"
)

// The browser sign-in over a real DynamoDB API (LocalStack): the cookie's
// pointer is written, resolved, ended and expired there as over the fake. It
// runs where ACCESS_ROSTER_DYNAMODB_URL is set -- `just test-s3` -- and skips
// elsewhere.
func TestSSOCookieOnDynamoDB(t *testing.T) {
	url := localstack(t)
	s, _ := tables(t, url, nil).Store(port.ModuleOIDC)
	dynamoport.SSOCookieFlow(t, s.Set(), s.Advance)
}
