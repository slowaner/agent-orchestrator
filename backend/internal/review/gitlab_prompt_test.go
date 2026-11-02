package review

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewTextsGitLabUsesReviewerDirectRoutes(t *testing.T) {
	prompt, system := reviewTexts(LaunchSpec{WorkerID: "worker-1", RunID: "run-1", PRURL: "https://gitlab.example/group/subgroup/repo/-/merge_requests/39", TargetSHA: "reviewed-head"})

	assert.Contains(t, prompt, "gitlab_mr_draft_note_create")
	assert.Contains(t, prompt, "gitlab_mr_draft_note_publish_all")
	assert.Contains(t, prompt, "full project path (including every subgroup)")
	assert.Contains(t, prompt, "Immediately before EACH write")
	assert.Contains(t, prompt, "changed head blocks every write")
	assert.Contains(t, prompt, "Existing drafts not created and acknowledged in this current attempt block publication")
	assert.Contains(t, prompt, "With no findings, still list drafts")
	assert.Contains(t, prompt, "reviewer_state=reviewed")
	assert.Contains(t, prompt, "not formal GitLab approvals")
	assert.Contains(t, prompt, "Leave githubReviewId empty for GitLab")
	assert.Contains(t, prompt, "publicationError")
	assert.Contains(t, prompt, "ao-review run=run-1 head=reviewed-head")
	assert.Contains(t, prompt, "https://gitlab.example/group/subgroup/repo/-/merge_requests/39")
	assert.Contains(t, system, "Do not push commits")
	assert.NotContains(t, prompt, "gh api")
	assert.NotContains(t, prompt, "gh pr review")
	assert.NotContains(t, prompt, "ao_gitlab_review_publish")
}

// These are prompt-contract checks, not execution of the reviewer's provider
// calls. Fault injection at a live native MCP boundary remains separate evidence.
func TestReviewTextsGitLabUncertainWriteInstructions(t *testing.T) {
	prompt, _ := reviewTexts(LaunchSpec{RunID: "run-1", PRURL: "https://gitlab.example/a/b/-/merge_requests/1", TargetSHA: "sha-1"})

	for _, outcome := range []string{"timeout", "transport error", "HTTP 5xx", "undecodable response", "missing draft ID", "missing success confirmation"} {
		t.Run(outcome, func(t *testing.T) {
			assert.Contains(t, prompt, outcome)
		})
	}
	assert.Contains(t, prompt, "Stop writes for this run")
	assert.Contains(t, prompt, "Do not repeat a POST or switch tools")
	assert.Contains(t, prompt, "Reconcile READ-ONLY with the same run/head marker")
	assert.Contains(t, prompt, "An empty draft list alone does not prove publication or failure")
	assert.Contains(t, prompt, "local verdict, provider publication and worker delivery status separate")
	assert.Contains(t, prompt, "non-individual, resolvable, unresolved and positioned")
	assert.Contains(t, prompt, "A summary alone does not reach the worker")
	assert.Contains(t, prompt, "inherited permissions deny it")
	assert.Contains(t, prompt, "do not change configuration or approvals")
}

func TestReviewTextsMixedQueueKeepsProviderWorkflowsIsolated(t *testing.T) {
	github := ports.ReviewTask{RunID: "gh-run", PRURL: "https://github.com/o/r/pull/7", TargetSHA: "gh-head"}
	gitlab := ports.ReviewTask{RunID: "gl-run", PRURL: "https://gitlab.example/g/sub/r/-/merge_requests/7", TargetSHA: "gl-head"}
	for _, tc := range []struct {
		name  string
		queue []ports.ReviewTask
	}{
		{name: "github first", queue: []ports.ReviewTask{github, gitlab}},
		{name: "gitlab first", queue: []ports.ReviewTask{gitlab, github}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt, _ := reviewTexts(LaunchSpec{WorkerID: "worker-1", PRURL: tc.queue[0].PRURL, ReviewQueue: tc.queue})

			_, gh, ok := strings.Cut(prompt, "BEGIN GitHub task gh-run\n")
			require.True(t, ok)
			gh, _, ok = strings.Cut(gh, "\nEND GitHub task gh-run")
			require.True(t, ok)
			assert.Contains(t, gh, "gh api --method POST")
			assert.Contains(t, gh, github.PRURL)
			assert.Contains(t, gh, github.TargetSHA)
			assert.Contains(t, gh, "githubReviewId")
			assert.NotContains(t, gh, gitlab.PRURL)
			assert.NotContains(t, gh, "gitlab_mr_")

			_, gl, ok := strings.Cut(prompt, "BEGIN GitLab task gl-run\n")
			require.True(t, ok)
			gl, _, ok = strings.Cut(gl, "\nEND GitLab task gl-run")
			require.True(t, ok)
			assert.Contains(t, gl, "gitlab_mr_draft_note_publish_all")
			assert.Contains(t, gl, gitlab.PRURL)
			assert.Contains(t, gl, "ao-review run=gl-run head=gl-head")
			assert.NotContains(t, gl, github.PRURL)
			assert.NotContains(t, gl, "gh api")
		})
	}
}

func TestGitLabCommandsBindSelfHostedSubgroupTarget(t *testing.T) {
	// URL parsing/command data only; no process or external request is made.
	prompt, _ := reviewTexts(LaunchSpec{PRURL: "https://gitlab.example:8443/group/subgroup/repo/-/merge_requests/39", RunID: "gl-run", TargetSHA: "gl-head"})
	target := "'projects/group%2Fsubgroup%2Frepo/merge_requests/39"
	host := "glab api --hostname 'gitlab.example:8443'"

	assert.Contains(t, prompt, host+" --method GET "+target+"'")
	assert.Contains(t, prompt, host+" --method GET "+target+"/draft_notes?per_page=100' --paginate")
	assert.Contains(t, prompt, host+" --method GET "+target+"/notes?per_page=100' --paginate")
	assert.Contains(t, prompt, host+" --method GET "+target+"/discussions?per_page=100' --paginate")
	assert.Contains(t, prompt, host+" --method POST "+target+"/draft_notes' --header 'Content-Type: application/json' --input - --include")
	assert.Contains(t, prompt, host+" --method POST "+target+"/draft_notes/bulk_publish' --header 'Content-Type: application/json' --input - --include")
	assert.NotContains(t, prompt, "glab mr approve")
	assert.NotContains(t, prompt, "glab mr merge")
	assert.NotContains(t, prompt, "glab auth")
	assert.NotContains(t, prompt, "--hostname 'gitlab.com'")
}

func TestGitLabMalformedTargetsHaveNoPublicationCommands(t *testing.T) {
	// These values exercise URL rejection without network or process execution.
	for _, target := range []string{
		"https://gitlab.example/g/r/-/merge_requests/0",
		"https://gitlab.example/g/r/-/merge_requests/1?project=other",
		"https://gitlab.example/g/r/-/merge_requests/1#fragment",
		"https://user@gitlab.example/g/r/-/merge_requests/1",
		"https://github.com/g/r/-/merge_requests/1",
	} {
		t.Run(target, func(t *testing.T) {
			prompt, _ := reviewTexts(LaunchSpec{PRURL: target, RunID: "gl-run", WorkerID: "worker-1"})

			assert.Contains(t, prompt, "Provider publication is blocked: invalid GitLab MR URL")
			assert.Contains(t, prompt, "ao review submit --session worker-1")
			assert.NotContains(t, prompt, "glab api")
			assert.NotContains(t, prompt, "gitlab_mr_draft_note_create")
			assert.NotContains(t, prompt, "gh api")
		})
	}
}
