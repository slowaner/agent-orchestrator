package review

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestReviewerApprovalScopeOnlyBindsOneGitLabRunAndRestoresRunningHistory(t *testing.T) {
	base := LaunchSpec{RunID: "run-1", ReviewSessionID: "review-1", BatchID: "batch-1", WorkerID: "worker-1", PRURL: "https://gitlab.example.test/group/repo/-/merge_requests/1", TargetSHA: "0123456789012345678901234567890123456789"}
	for _, test := range []struct {
		name   string
		change func(*LaunchSpec)
		valid  bool
	}{
		{"single", func(*LaunchSpec) {}, true},
		{"github", func(s *LaunchSpec) { s.PRURL = "https://github.com/group/repo/pull/1" }, false},
		{"mixed", func(s *LaunchSpec) {
			s.ReviewQueue = []ports.ReviewTask{{RunID: s.RunID, PRURL: s.PRURL, TargetSHA: s.TargetSHA}, {RunID: "github-run", PRURL: "https://github.com/group/repo/pull/1", TargetSHA: s.TargetSHA}}
		}, false},
		{"mismatched-task", func(s *LaunchSpec) {
			s.ReviewQueue = []ports.ReviewTask{{RunID: "other-run", PRURL: s.PRURL, TargetSHA: s.TargetSHA}}
		}, false},
		{"restore-running", func(s *LaunchSpec) {
			s.PreviousRuns = []domain.ReviewRun{{ID: s.RunID, ReviewID: s.ReviewSessionID, SessionID: s.WorkerID, BatchID: s.BatchID, PRURL: s.PRURL, TargetSHA: s.TargetSHA, Status: domain.ReviewRunRunning}}
			s.RunID, s.PRURL, s.TargetSHA = "", "", ""
		}, true},
		{"restore-completed", func(s *LaunchSpec) {
			s.PreviousRuns = []domain.ReviewRun{{ID: s.RunID, ReviewID: s.ReviewSessionID, SessionID: s.WorkerID, PRURL: s.PRURL, TargetSHA: s.TargetSHA, Status: domain.ReviewRunComplete}}
			s.RunID = ""
		}, false},
		{"restore-multiple", func(s *LaunchSpec) {
			s.PreviousRuns = []domain.ReviewRun{{Status: domain.ReviewRunRunning}, {Status: domain.ReviewRunRunning}}
			s.RunID = ""
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			test.change(&spec)
			scope := reviewerApprovalScope(spec)
			if !test.valid {
				require.Nil(t, scope)
				return
			}
			require.NotNil(t, scope)
			require.Equal(t, base.RunID, scope.RunID)
			require.Equal(t, base.BatchID, scope.BatchID)
			require.Equal(t, base.PRURL, scope.PRURL)
			require.Equal(t, base.TargetSHA, scope.TargetSHA)
		})
	}
}
