package console

import (
	"context"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/access"
	"github.com/truvity/sluis/internal/slackroster/connection"
	"github.com/truvity/sluis/internal/slackroster/status"
	"github.com/truvity/sluis/policy"
)

// What the channel pages that have not moved into the module yet still ask of
// it. Each goes when the page that uses it does.

// CheckSources is the rule on a channel's sources and individual members.
func (c *Service) CheckSources(ctx context.Context, sources, members []string, owner string, ordinary bool) error {
	return c.checkSources(ctx, sources, members, owner, ordinary)
}

// SourceDirectories are the connected directories and their groups, as a picker
// lists them.
func (c *Service) SourceDirectories(ctx context.Context, only ...string) ([]*directoryrosterv1.SlackSourceDirectory, string) {
	return c.sourceDirectories(ctx, only...)
}

// DiscoveredChannels merges every connected workspace's discovered shared
// channels into one row per channel.
func DiscoveredChannels(
	id access.Identity, book Book, p policy.Policy, reports map[string]status.Workspace, records []connection.SharedRecord, available bool,
) []*directoryrosterv1.SlackDiscoveredChannel {
	return discoveredChannels(id, book, p, reports, records, available)
}

// DiscoveredIn is the host workspace's own sighting of a channel id.
func DiscoveredIn(reports map[string]status.Workspace, workspace, channelID string) (status.Discovered, bool) {
	return discoveredIn(reports, workspace, channelID)
}
