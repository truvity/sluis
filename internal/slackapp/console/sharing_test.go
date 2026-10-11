package console

import (
	"testing"

	"github.com/truvity/sluis/internal/slackapp"
)

const acmeTeam = "T0123ABCD"

func TestOnlyAnotherTeamCountsAsSharing(t *testing.T) {
	own := slackapp.Channel{
		ConnectedTeamIDs: []string{acmeTeam}, InternalTeamIDs: []string{acmeTeam}, SharedTeamIDs: []string{acmeTeam}, ConversationHostID: acmeTeam,
	}
	if slackShared(own, acmeTeam) {
		t.Error("an ordinary channel listing only its own team is shared")
	}
	other := slackapp.Channel{InternalTeamIDs: []string{acmeTeam, "TOTHER"}}
	if !slackShared(other, acmeTeam) {
		t.Error("a channel listing another team in internal_team_ids is not shared")
	}
	if !slackShared(slackapp.Channel{IsExtShared: true}, acmeTeam) {
		t.Error("is_ext_shared is not shared")
	}
	if !slackShared(own, "") {
		t.Error("with the own team unknown, a listed id must count")
	}
	if slackShared(slackapp.Channel{}, "") {
		t.Error("an empty channel is shared")
	}
}
