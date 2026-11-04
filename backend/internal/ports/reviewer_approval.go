package ports

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ReviewerApprovalConversation rebinds an idle native reviewer between passes.
// An active turn or pending interaction must prevent rebinding.
type ReviewerApprovalConversation interface {
	// SetReviewerApprovalScope binds an idle conversation to a validated run.
	// Active turns and pending interactions prevent rebinding. A nil scope
	// removes reviewer approval authority without granting permissions or
	// prohibiting ordinary questions outside a scoped reviewer turn.
	SetReviewerApprovalScope(context.Context, *ReviewerApprovalScope) error
}

// ReviewerApprovalScope binds human approval requests to one GitLab review run.
// It is launch context, not a permission grant or proof of provider execution.
type ReviewerApprovalScope struct {
	// ReviewID identifies the durable reviewer conversation owner.
	ReviewID string
	// RunID identifies the review pass authorized to request human decisions.
	RunID string
	// BatchID optionally groups passes from the same review trigger.
	BatchID string
	// WorkerID identifies the worker whose change request is reviewed.
	WorkerID domain.SessionID
	// PRURL identifies the exact GitLab host, project and merge request.
	PRURL string
	// TargetSHA is the full reviewed head commit binding this run's requests.
	TargetSHA string
}

// Valid reports whether the scope names a complete GitLab review target.
func (s ReviewerApprovalScope) Valid() bool {
	ref, err := domain.ParseChangeRequestURL(s.PRURL)
	return err == nil && ref.Provider == "gitlab" && s.ReviewID != "" && s.RunID != "" && s.WorkerID != "" && s.TargetSHA != ""
}
