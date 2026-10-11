package console

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	directoryrosterv1 "github.com/truvity/sluis/gen/directoryroster/v1"
	"github.com/truvity/sluis/internal/hub"
)

// Stamp is a time as the contract carries it: the zero time is nil.
func Stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// BackendKind turns the contract's enum into a backend's own name.
func BackendKind(b directoryrosterv1.Backend) string {
	switch b {
	case directoryrosterv1.Backend_BACKEND_GOOGLE:
		return "google"
	case directoryrosterv1.Backend_BACKEND_ENTRA:
		return "entra"
	case directoryrosterv1.Backend_BACKEND_DEMO:
		return "demo"
	case directoryrosterv1.Backend_BACKEND_UNSPECIFIED:
		return ""
	default:
		return ""
	}
}

// BackendEnum is BackendKind's inverse.
func BackendEnum(kind string) directoryrosterv1.Backend {
	switch kind {
	case "google":
		return directoryrosterv1.Backend_BACKEND_GOOGLE
	case "entra":
		return directoryrosterv1.Backend_BACKEND_ENTRA
	case "demo", "fake":
		return directoryrosterv1.Backend_BACKEND_DEMO
	default:
		return directoryrosterv1.Backend_BACKEND_UNSPECIFIED
	}
}

func credentialEnum(c hub.CredentialType) directoryrosterv1.CredentialType {
	switch c {
	case hub.CredentialOAuth:
		return directoryrosterv1.CredentialType_CREDENTIAL_TYPE_OAUTH_REFRESH_TOKEN
	case hub.CredentialServiceAccountKey:
		return directoryrosterv1.CredentialType_CREDENTIAL_TYPE_SERVICE_ACCOUNT_KEY
	default:
		return directoryrosterv1.CredentialType_CREDENTIAL_TYPE_UNSPECIFIED
	}
}

// reasonEnum turns the hub's reason into the contract's.
func reasonEnum(r hub.DomainReason) directoryrosterv1.DomainReason {
	switch r {
	case hub.ReasonContested:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_CONTESTED
	case hub.ReasonFirstSnapshotPending:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_FIRST_SNAPSHOT_PENDING
	case hub.ReasonProbeFailed:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_PROBE_FAILED
	case hub.ReasonSnapshotStale:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_SNAPSHOT_STALE
	case hub.ReasonNone:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_UNSPECIFIED
	default:
		return directoryrosterv1.DomainReason_DOMAIN_REASON_UNSPECIFIED
	}
}

func DomainsProto(domains []hub.DomainStanding) []*directoryrosterv1.WorkspaceDomain {
	out := make([]*directoryrosterv1.WorkspaceDomain, 0, len(domains))
	for _, d := range domains {
		out = append(out, &directoryrosterv1.WorkspaceDomain{
			Name:          d.Name,
			Authoritative: d.Authoritative,
			Conflict:      d.Conflict,
			Served:        d.Served,
			Owned:         d.Owned,
			Reason:        reasonEnum(d.Reason),
		})
	}
	return out
}

func WorkspaceProto(v *hub.WorkspaceView) *directoryrosterv1.Workspace {
	ws := v.Workspace
	return &directoryrosterv1.Workspace{
		Id:          ws.ID,
		Backend:     BackendEnum(ws.Backend),
		Domains:     DomainsProto(v.Domains),
		Admin:       ws.Admin,
		Credential:  credentialEnum(ws.Credential),
		ConnectedBy: ws.ConnectedBy,
		ConnectedAt: Stamp(ws.ConnectedAt),
		Health: &directoryrosterv1.Health{
			ProbedAt: Stamp(ws.Health.ProbedAt),
			Ok:       ws.Health.OK,
			Error:    ws.Health.Error,
		},
		SnapshotAt:       Stamp(v.SnapshotAt),
		Declared:         ws.Declared,
		SyncGroups:       ws.SyncGroups,
		DiscoveredGroups: v.Discovered,
	}
}
