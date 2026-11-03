package scm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/stretchr/testify/require"
)

func TestReviewTraceCoalescesAndRetainsRequestsForNextPoll(t *testing.T) {
	o := New(&fakeProvider{}, testStoreWithSession(), nil, Config{})
	a := ports.ReviewRefreshRequest{RunID: "run-a", WorkerID: "p-1", PRURL: "mr-a"}
	b := a
	b.RunID = "run-b"
	o.RequestReviewRefresh(t.Context(), a)
	o.RequestReviewRefresh(t.Context(), b)
	flags, requests := o.takeForcedReviews()
	require.True(t, flags["mr-a"])
	require.ElementsMatch(t, []ports.ReviewRefreshRequest{a, b}, requests)
	// A request after atomic consumption belongs to the following poll.
	c := a
	c.RunID, c.PRURL = "run-c", "mr-b"
	o.RequestReviewRefresh(t.Context(), c)
	flags, requests = o.takeForcedReviews()
	require.Equal(t, map[string]bool{"mr-b": true}, flags)
	require.Equal(t, []ports.ReviewRefreshRequest{c}, requests)
	flags, requests = o.takeForcedReviews()
	require.Empty(t, flags)
	require.Empty(t, requests)
}

func TestReviewTracePollSeparatesTargetsAndUnrelatedRequests(t *testing.T) {
	store := testStoreWithSession()
	first, second := knownPR(1), knownPR(2)
	store.prs["p-1"] = []domain.PullRequest{first, second}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1), prKey(testRepo, 2): testObs(2)},
		reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): {Threads: []ports.SCMReviewThreadObservation{{ID: "thread-1"}}}, prKey(testRepo, 2): {Threads: []ports.SCMReviewThreadObservation{{ID: "thread-2"}}}},
	}
	var output bytes.Buffer
	o := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(200, 0).UTC())
	o.logger = slog.New(slog.NewJSONHandler(&output, nil))
	o.traceEvents = true
	o.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	o.Cache.LastSyncCursor[prKey(testRepo, 0)] = time.Unix(190, 0).UTC()
	for _, request := range []ports.ReviewRefreshRequest{
		{RunID: "first-a", WorkerID: "p-1", PRURL: first.URL},
		{RunID: "first-b", WorkerID: "p-1", PRURL: first.URL},
		{RunID: "second", WorkerID: "p-1", PRURL: second.URL},
		{RunID: "wrong-worker", WorkerID: "p-2", PRURL: first.URL},
		{RunID: "untracked", WorkerID: "p-1", PRURL: "untracked"},
	} {
		o.RequestReviewRefresh(t.Context(), request)
	}
	output.Reset()
	require.NoError(t, o.Poll(t.Context()))
	require.Equal(t, 2, provider.reviewCalls)
	counts := map[string]int{}
	expectedTarget := map[string]string{"first-a": first.URL, "first-b": first.URL, "second": second.URL}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		if event["event"] != "provider_fetch" && event["event"] != "observation_commit" && event["event"] != "lifecycle_requested" {
			continue
		}
		run, _ := event["run_id"].(string)
		require.Contains(t, []string{"first-a", "first-b", "second"}, run)
		require.Equal(t, "p-1", event["worker_id"])
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(expectedTarget[run]))), event["target_key"])
		counts[run]++
	}
	require.GreaterOrEqual(t, counts["first-a"], 4)
	require.Equal(t, counts["first-a"], counts["first-b"])
	require.GreaterOrEqual(t, counts["second"], 4)
	output.Reset()
	require.NoError(t, o.Poll(t.Context()))
	require.NotContains(t, output.String(), "run_id")
}

func TestReviewTraceRequestDuringFetchRunsOnFollowingPoll(t *testing.T) {
	store := testStoreWithSession()
	local := knownPR(1)
	store.prs["p-1"] = []domain.PullRequest{local}
	provider := &fakeProvider{
		repoGuards:   map[string]ports.SCMGuardResult{prKey(testRepo, 0): {ETag: "repo", NotModified: true}},
		observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
	}
	var output bytes.Buffer
	o := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(200, 0).UTC())
	o.logger, o.traceEvents = slog.New(slog.NewJSONHandler(&output, nil)), true
	o.Cache.RepoPRListETag[prKey(testRepo, 0)] = "repo"
	o.Cache.LastSyncCursor[prKey(testRepo, 0)] = time.Unix(190, 0).UTC()
	o.RequestReviewRefresh(t.Context(), ports.ReviewRefreshRequest{RunID: "before", WorkerID: "p-1", PRURL: local.URL})
	provider.onReview = func() {
		o.RequestReviewRefresh(t.Context(), ports.ReviewRefreshRequest{RunID: "during", WorkerID: "p-1", PRURL: local.URL})
		provider.onReview = nil
	}
	output.Reset()
	require.NoError(t, o.Poll(t.Context()))
	require.Equal(t, 1, provider.reviewCalls)
	require.Contains(t, output.String(), `"event":"observation_commit","outcome":"unchanged"`)
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.Contains(line, `"event":"provider_fetch"`) {
			require.NotContains(t, line, `"run_id":"during"`)
		}
	}
	output.Reset()
	require.NoError(t, o.Poll(t.Context()))
	require.Equal(t, 2, provider.reviewCalls)
	require.Contains(t, output.String(), `"run_id":"during"`)
	require.NotContains(t, output.String(), `"run_id":"before"`)
}

func TestReviewTraceRejectsCancelledOrStoppedRequests(t *testing.T) {
	var output bytes.Buffer
	o := New(nil, nil, nil, Config{Logger: slog.New(slog.NewJSONHandler(&output, nil)), TraceEvents: true})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := ports.ReviewRefreshRequest{RunID: "run", WorkerID: "worker", PRURL: "mr"}
	o.RequestReviewRefresh(ctx, request)
	require.Contains(t, output.String(), `"event":"refresh_dropped","outcome":"cancelled"`)
	flags, requests := o.takeForcedReviews()
	require.Empty(t, flags)
	require.Empty(t, requests)
	output.Reset()
	o.stopped = true
	o.RequestReviewRefresh(t.Context(), request)
	require.Contains(t, output.String(), `"event":"refresh_dropped","outcome":"cancelled"`)
	flags, requests = o.takeForcedReviews()
	require.Empty(t, flags)
	require.Empty(t, requests)
}

func TestReviewTraceReportsFetchAndPersistenceFailuresWithoutPayloads(t *testing.T) {
	for _, stage := range []string{"provider_fetch", "observation_commit"} {
		t.Run(stage, func(t *testing.T) {
			store := testStoreWithSession()
			local := knownPR(1)
			store.prs["p-1"] = []domain.PullRequest{local}
			provider := &fakeProvider{
				observations: map[string]ports.SCMObservation{prKey(testRepo, 1): testObs(1)},
				reviews:      map[string]ports.SCMReviewObservation{prKey(testRepo, 1): {Threads: []ports.SCMReviewThreadObservation{{ID: "changed-thread", Comments: []ports.SCMReviewCommentObservation{{ID: "note", Author: "reviewer", Body: "finding"}}}}}},
			}
			failure := errors.New("private diagnostic detail")
			if stage == "provider_fetch" {
				provider.reviewErr = failure
			} else {
				store.writeErr = failure
			}
			var output bytes.Buffer
			o := newTestObserver(store, provider, &fakeLifecycle{}, time.Unix(200, 0).UTC())
			o.logger, o.traceEvents = slog.New(slog.NewJSONHandler(&output, nil)), true
			o.RequestReviewRefresh(t.Context(), ports.ReviewRefreshRequest{RunID: "run", WorkerID: "p-1", PRURL: local.URL})
			require.NoError(t, o.Poll(t.Context()))
			found := false
			for _, line := range strings.Split(output.String(), "\n") {
				if !strings.Contains(line, `"msg":"review trace"`) {
					continue
				}
				require.NotContains(t, line, failure.Error())
				require.NotContains(t, line, local.URL)
				var event map[string]any
				require.NoError(t, json.Unmarshal([]byte(line), &event))
				if event["event"] == stage && event["outcome"] == "failed" {
					require.Equal(t, "run", event["run_id"])
					found = true
				}
			}
			require.True(t, found, "missing explicit failure event")
		})
	}
}
