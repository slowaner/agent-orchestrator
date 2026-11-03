package ports

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

// ReviewRefreshRequest correlates one confirmed reviewer receipt with a
// best-effort read refresh. It does not authorize provider writes or attest to
// provider publication or worker receipt.
type ReviewRefreshRequest struct {
	// RunID is the correlation token for the persisted review result.
	RunID string
	// WorkerID is the worker whose tracked target should be refreshed.
	WorkerID domain.SessionID
	// PRURL is the target to read; trace output hashes it rather than logging it.
	PRURL string
	// TargetSHA identifies the reviewed head, not an override of provider state.
	TargetSHA string
}
