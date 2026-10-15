package rpc

import (
	"context"

	"github.com/truvity/sluis/internal/backup/restore"
	"github.com/truvity/sluis/internal/backup/restorejob"
	"github.com/truvity/sluis/internal/modcall"
)

// The restore function's module and methods. It is the same zip as the backup
// module's, deployed a second time (docs/decisions/0072), and answers two
// reads: how the restore stands, and what restoring a backup would do. Nothing
// here starts or resumes a restore: that is an invocation of the function
// itself, which only an administrator or the break-glass role may make
// (internal/lambdaapp), with their own credentials and never through the
// issuer, so that it works when the issuer is broken.
const (
	RestoreModule        = "restore"
	MethodRestoreStatus  = "status"
	MethodRestorePreview = "preview"
)

// RestoreService is what the restore methods call. *App of
// internal/backup/app is one.
type RestoreService interface {
	RestoreStatus(ctx context.Context) (restorejob.Status, error)
	PreviewRestore(ctx context.Context, backupID string) (*restore.Report, error)
}

// PreviewRequest names the backup to preview.
type PreviewRequest struct {
	Backup string `json:"backup"`
}

// PreviewOutcome is the outcome of a preview that produced a report.
const PreviewOutcome = "preview"

// PreviewResponse is `restore.preview`: a report of counts, or a refusal. The
// report never carries the records that differ (their names or their values):
// a caller that wants them runs the preview directly.
type PreviewResponse struct {
	Outcome string          `json:"outcome"`
	Backup  string          `json:"backup,omitempty"`
	Error   string          `json:"error,omitempty"`
	Report  *restore.Report `json:"report,omitempty"`
}

// previewRefused is what a caller is told when the backup cannot be previewed.
// The cause stays in the function's log: the caller is another module's trust
// domain.
const previewRefused = "the backup could not be previewed; see the restore function's log, or preview it directly with your own credentials"

// RegisterRestore makes svc answer `restore.status` and `restore.preview` on s:
// the console, an administrator and the break-glass role may call them, and no
// caller may do more.
func RegisterRestore(s *modcall.Server, svc RestoreService) {
	modcall.Handle(s, MethodRestoreStatus, func(ctx context.Context, _ StatusRequest) (restorejob.Status, error) {
		return svc.RestoreStatus(ctx)
	}, modcall.Allow(CallerConsole, CallerAdmin, CallerBreakglass))
	modcall.Handle(s, MethodRestorePreview, func(ctx context.Context, r PreviewRequest) (PreviewResponse, error) {
		rep, err := svc.PreviewRestore(ctx, r.Backup)
		if err != nil {
			return PreviewResponse{Outcome: restorejob.OutcomeRefused, Backup: r.Backup, Error: previewRefused}, nil
		}
		trimmed := *rep
		trimmed.Sections = make([]restore.SectionReport, len(rep.Sections))
		for i, sec := range rep.Sections {
			sec.Items, sec.Truncated = nil, false
			trimmed.Sections[i] = sec
		}
		return PreviewResponse{Outcome: PreviewOutcome, Backup: r.Backup, Report: &trimmed}, nil
	}, modcall.Allow(CallerConsole, CallerAdmin, CallerBreakglass))
}
