package chat

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type reviewerApprovalStore interface {
	GetReviewByID(context.Context, string) (domain.Review, bool, error)
	GetReviewRun(context.Context, string) (domain.ReviewRun, bool, error)
}

func (s *Service) validateReviewerApproval(ctx context.Context, owner domain.ConversationOwner, workerID domain.SessionID, scope *ports.ReviewerApprovalScope) error {
	if scope == nil {
		return nil
	}
	if !scope.Valid() || owner != domain.ReviewConversationOwner(scope.ReviewID) || workerID != scope.WorkerID {
		return fmt.Errorf("%w: invalid reviewer approval owner", ports.ErrChatRequestNotPending)
	}
	store, ok := s.store.(reviewerApprovalStore)
	if !ok {
		return fmt.Errorf("%w: reviewer approval run store unavailable", ports.ErrChatRequestNotPending)
	}
	review, found, err := store.GetReviewByID(ctx, scope.ReviewID)
	if err != nil {
		return fmt.Errorf("read reviewer approval owner: %w", err)
	}
	if !found || review.IsArchived || review.SessionID != workerID {
		return fmt.Errorf("%w: reviewer approval owner is no longer active", ports.ErrChatRequestNotPending)
	}
	run, found, err := store.GetReviewRun(ctx, scope.RunID)
	if err != nil {
		return fmt.Errorf("read reviewer approval run: %w", err)
	}
	if !found || run.Status != domain.ReviewRunRunning || run.ReviewID != scope.ReviewID || run.SessionID != workerID || run.BatchID != scope.BatchID || run.PRURL != scope.PRURL || run.TargetSHA != scope.TargetSHA {
		return fmt.Errorf("%w: reviewer approval run is stale or mismatched", ports.ErrChatRequestNotPending)
	}
	return nil
}

func (s *Service) validateCurrentReviewerApproval(ctx context.Context, owner domain.ConversationOwner, permissionDecision bool) error {
	s.mu.RLock()
	cfg := s.startConfigs[owner]
	s.mu.RUnlock()
	if permissionDecision && owner.Kind == domain.ConversationOwnerReview && cfg.Harness == domain.HarnessCodex && cfg.ReadOnly && cfg.ReviewerApproval == nil {
		return fmt.Errorf("%w: read-only reviewer has no active approval scope", ports.ErrChatRequestNotPending)
	}
	return s.validateReviewerApproval(ctx, owner, cfg.SessionID, cfg.ReviewerApproval)
}

func rebindReviewerApproval(ctx context.Context, controller *Controller, previous, next *ports.ReviewerApprovalScope) error {
	if previous != nil && next != nil && *previous == *next {
		return nil
	}
	updater, ok := controller.conv.(ports.ReviewerApprovalConversation)
	if !ok {
		return fmt.Errorf("native reviewer cannot rebind approval scope between passes")
	}
	return updater.SetReviewerApprovalScope(ctx, next)
}
