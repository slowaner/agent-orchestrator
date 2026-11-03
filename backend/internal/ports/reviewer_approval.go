package ports

import (
	"context"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ReviewerApprovalConversation rebinds an idle native reviewer between passes.
// An active turn or pending interaction must prevent rebinding.
type ReviewerApprovalConversation interface {
	SetReviewerApprovalScope(context.Context, *ReviewerApprovalScope) error
}

// ReviewerApprovalScope binds human approval requests to one GitLab review run.
// It is launch context, not a permission grant or proof of provider execution.
type ReviewerApprovalScope struct {
	ReviewID  string
	RunID     string
	BatchID   string
	WorkerID  domain.SessionID
	PRURL     string
	TargetSHA string
}

// Valid reports whether the scope names a complete GitLab review target.
func (s ReviewerApprovalScope) Valid() bool {
	ref, err := domain.ParseChangeRequestURL(s.PRURL)
	return err == nil && ref.Provider == "gitlab" && s.ReviewID != "" && s.RunID != "" && s.WorkerID != "" && s.TargetSHA != ""
}
