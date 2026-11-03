package review

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func reviewerApprovalScope(spec LaunchSpec) *ports.ReviewerApprovalScope {
	if spec.RunID == "" {
		// Restore has history rather than a queued prompt. Only a single still
		// running run can recover approval context; completed history grants none.
		var running []domain.ReviewRun
		for _, run := range spec.PreviousRuns {
			if run.Status == domain.ReviewRunRunning {
				running = append(running, run)
			}
		}
		if len(running) != 1 {
			return nil
		}
		run := running[0]
		if run.ReviewID != spec.ReviewSessionID || run.SessionID != spec.WorkerID {
			return nil
		}
		spec.RunID, spec.BatchID, spec.PRURL, spec.TargetSHA = run.ID, run.BatchID, run.PRURL, run.TargetSHA
	}
	if len(spec.ReviewQueue) > 1 {
		return nil
	}
	if len(spec.ReviewQueue) == 1 {
		task := spec.ReviewQueue[0]
		if task.RunID != spec.RunID || task.PRURL != spec.PRURL || task.TargetSHA != spec.TargetSHA {
			return nil
		}
	}
	scope := ports.ReviewerApprovalScope{ReviewID: spec.ReviewSessionID, RunID: spec.RunID, BatchID: spec.BatchID, WorkerID: spec.WorkerID, PRURL: spec.PRURL, TargetSHA: spec.TargetSHA}
	if !scope.Valid() {
		return nil
	}
	return &scope
}
