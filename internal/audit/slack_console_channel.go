package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/truvity/sluis/audit/sdk/record"
)

// SlackConsoleChannel is an ordinary channel managed from the console as the
// trail names it: by workspace and channel name, with the directory groups
// that feed it (targets, because an address is an identifier and never data)
// and how it is reconciled.
type SlackConsoleChannel struct {
	Workspace string
	Name      string
	Private   bool
	// Mode is extend or strict.
	Mode    string
	Sources []string
	// Members are the individual addresses listed beside the groups: targets
	// too, never data.
	Members []string
}

func (c SlackConsoleChannel) targets() []*record.Target {
	out := append([]*record.Target{targetSlackWorkspace(c.Workspace), targetSlackChannel(c.Workspace, c.Name)}, directoryGroupTargets(c.Sources)...)
	return append(out, directoryUserTargets(c.Members)...)
}

func (c SlackConsoleChannel) data(changes string) data {
	mode := c.Mode
	if mode == "" {
		mode = "extend"
	}
	// The schema still declares `reason`, which records written under
	// catalogue 1.3.0 to 1.5.0 carry: it is read, never written.
	return data{
		"name": c.Name, "privacy": visibility(c.Private), "mode": mode,
		"sources": len(c.Sources), "members": len(c.Members), "changes": changes,
	}
}

// SlackConsoleChannelCreated is an ordinary channel put under management
// from the console.
func SlackConsoleChannelCreated(actor Actor, c SlackConsoleChannel) *record.Record {
	return build("roster.slack_console_channel.created", actor, Succeeded(), nil, c.targets(), c.data(""))
}

// SlackConsoleChannelUpdated is a console channel's record changed from the
// console; changes says what, as 'field: before -> after' parts.
func SlackConsoleChannelUpdated(actor Actor, c SlackConsoleChannel, changes string) *record.Record {
	return build("roster.slack_console_channel.updated", actor, Succeeded(), nil, c.targets(), c.data(changes))
}

// SlackConsoleChannelDeleted is a console channel's record deleted from the
// console. The channel itself stays in Slack.
func SlackConsoleChannelDeleted(actor Actor, c SlackConsoleChannel) *record.Record {
	return build("roster.slack_console_channel.deleted", actor, Succeeded(), nil, c.targets(), c.data(""))
}

// directoryGroupTargets are the groups that feed a channel, as targets, in
// address order without repeats.
func directoryGroupTargets(groups []string) []*record.Target {
	out := make([]*record.Target, 0, len(groups))
	for _, g := range slices.Compact(slices.Sorted(slices.Values(groups))) {
		out = append(out, &record.Target{Type: "directory_group", Id: strings.ToLower(strings.TrimSpace(g))})
	}
	return out
}

// directoryUserTargets are the individually listed people of a channel, as
// targets, in address order without repeats.
//
// A directory_user is a person, and a deployment that declares external
// identifiers opaque refuses a person target that reads as something typed: an
// address does. The target is therefore a minted identifier, [DirectoryUserID],
// the same for the same address in every record, and the address stays in the
// channel's definition where the operator wrote it.
func directoryUserTargets(members []string) []*record.Target {
	addresses := make([]string, 0, len(members))
	for _, m := range members {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			addresses = append(addresses, m)
		}
	}
	out := make([]*record.Target, 0, len(addresses))
	for _, m := range slices.Compact(slices.Sorted(slices.Values(addresses))) {
		out = append(out, &record.Target{Type: "directory_user", Id: DirectoryUserID(m)})
	}
	return out
}

// DirectoryUserID is the identifier a record uses for a person of a directory
// who has no other one (no login, no Slack user id): "du_" and the first 128
// bits of the SHA-256 of the lower-cased address. It names nobody without the
// directory, holds no "@" or space, and is stable, so records about one person
// can be found together.
func DirectoryUserID(address string) string {
	sum := sha256.Sum256([]byte("directory_user\x00" + strings.ToLower(strings.TrimSpace(address))))
	return "du_" + hex.EncodeToString(sum[:16])
}
