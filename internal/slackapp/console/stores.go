package console

import (
	"context"

	"github.com/truvity/sluis/internal/slackapp"
	slackcatalogue "github.com/truvity/sluis/internal/slackapp/catalogue"

	"github.com/truvity/sluis/internal/slackapp/catalogueapp"
	"github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/internal/slackroster/reconcile"
)

// CatalogueApps is where catalogue Slack Apps are kept: every App's record,
// client credentials and bot token, by its catalogue id.
type CatalogueApps interface {
	Put(ctx context.Context, record catalogueapp.Record, credentials catalogueapp.Credentials) error
	List(ctx context.Context) ([]catalogueapp.Record, error)
	// Get is one App's record and credentials, installed or not.
	Get(ctx context.Context, id string) (catalogueapp.Record, catalogueapp.Credentials, bool, error)
}

// ChannelRecords is where console channels' records are kept: the records the
// Slack controller reads.
type ChannelRecords interface {
	List(ctx context.Context) ([]connection.ChannelRecord, error)
	// Apply reads one record (nil when there is none), asks decide what it
	// becomes (nil deletes it) and writes that under the object's version.
	// decide is given every record as read, for a check across records.
	Apply(ctx context.Context, workspace, name string,
		decide func(current *reconcile.ConsoleChannel, all []connection.ChannelRecord) (*reconcile.ConsoleChannel, error)) error
}

// SharedRecords is where Slack Connect channel definitions are kept: the
// records the Slack controller reads.
type SharedRecords interface {
	List(ctx context.Context) ([]connection.SharedRecord, error)
	// Apply reads one record (nil when there is none), asks decide what it
	// becomes (nil deletes it) and writes that under the object's version.
	Apply(ctx context.Context, name string, decide func(current *reconcile.SharedChannel) (*reconcile.SharedChannel, error)) error
}

// StatusReports is what the Slack controller last reported, one document per
// workspace.
type StatusReports interface {
	Reports(ctx context.Context) (map[string]string, error)
}

// The types the console's configuration names without importing the Slack
// module's packages.
type (
	// APIOption configures the calls to Slack: a test points them at a fake.
	APIOption = slackapp.Option
	// Catalogue is every Slack App a deployment declares.
	Catalogue = slackcatalogue.Catalogue
)

// ValidAppID reports whether id is a catalogue App's id.
func ValidAppID(id string) bool { return slackcatalogue.ValidID(id) }
