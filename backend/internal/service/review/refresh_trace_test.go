package review

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestReceiptTraceOnlyRequestsConfirmedNewPublication(t *testing.T) {
	for _, status := range []string{"published", "blocked", "unconfirmed"} {
		t.Run(status, func(t *testing.T) {
			const target = "https://gitlab.example/g/p/-/merge_requests/1"
			store := &fakeStore{ok: true, run: domain.ReviewRun{ID: "run-1", SessionID: "worker", PRURL: target, TargetSHA: "head", Status: domain.ReviewRunRunning}}
			refresh := &recordingReviewRefresher{}
			var output bytes.Buffer
			service := New(nil, store, WithThreadRefresher(refresh), WithLocalReviewTrace(slog.New(slog.NewJSONHandler(&output, nil))))
			publication := &domain.ReviewPublication{Provider: "gitlab", Status: status, PRURL: target, TargetSHA: "head"}
			if status == "published" {
				publication.Refs = []domain.ReviewPublicationRef{{Kind: "note", ID: "101"}}
			} else {
				publication.Error = "provider unavailable"
			}
			input := SubmittedReview{RunID: "run-1", Verdict: domain.VerdictApproved, Body: "local verdict", Publication: publication}
			_, err := service.SubmitMany(t.Context(), "worker", []SubmittedReview{input})
			require.NoError(t, err)
			require.Equal(t, domain.ReviewRunComplete, store.run.Status)
			require.Contains(t, output.String(), `"event":"receipt_recorded","outcome":"`+status+`"`)
			require.NotContains(t, output.String(), target)
			require.NotContains(t, output.String(), "local verdict")
			if status == "published" {
				require.Len(t, refresh.prURLs, 1)
				require.Contains(t, output.String(), `"event":"refresh_requested"`)
			} else {
				require.Empty(t, refresh.prURLs)
				require.NotContains(t, output.String(), `"event":"refresh_requested"`)
			}
			output.Reset()
			_, err = service.SubmitMany(t.Context(), "worker", []SubmittedReview{input})
			require.NoError(t, err)
			require.Empty(t, output.String())
		})
	}
}
