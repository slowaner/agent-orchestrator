package review

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func hasGitLabReview(spec LaunchSpec) bool {
	if len(spec.ReviewQueue) == 0 {
		return strings.Contains(spec.PRURL, "/-/merge_requests/")
	}
	for _, task := range spec.ReviewQueue {
		if strings.Contains(task.PRURL, "/-/merge_requests/") {
			return true
		}
	}
	return false
}

// providerReviewTexts gives each queued run its own provider workflow. A mixed
// queue must not replace a GitHub task's publishing instructions with GitLab's.
func providerReviewTexts(spec LaunchSpec) (string, string) {
	tasks := spec.ReviewQueue
	if len(tasks) == 0 {
		tasks = []ports.ReviewTask{{RunID: spec.RunID, PRURL: spec.PRURL, TargetSHA: spec.TargetSHA}}
	}
	var prompt strings.Builder
	prompt.WriteString("Review every queued task. Apply only the provider workflow in that task's section; never reuse another task's target, head, draft batch or provider IDs. Record every local verdict even when its provider publication is blocked.\n\n")
	for _, task := range tasks {
		one := spec
		one.RunID, one.PRURL, one.TargetSHA = task.RunID, task.PRURL, task.TargetSHA
		one.ReviewQueue = nil
		provider := "GitHub"
		var text string
		if strings.Contains(task.PRURL, "/-/merge_requests/") {
			provider = "GitLab"
			text = gitLabReviewText(one)
		} else {
			text, _ = githubReviewTexts(one)
		}
		fmt.Fprintf(&prompt, "BEGIN %s task %s\n%s\nEND %s task %s\n\n", provider, task.RunID, text, provider, task.RunID)
	}
	return prompt.String(), reviewSystemPrompt()
}

func gitLabReviewText(spec LaunchSpec) string {
	ref, err := domain.ParseChangeRequestURL(spec.PRURL)
	if err != nil || ref.Provider != "gitlab" {
		return fmt.Sprintf(`Review the queued task locally.
%s
Provider publication is blocked: invalid GitLab MR URL. Do not issue provider writes or invent a provider ID. Record the local verdict and this publicationError with an empty githubReviewId:
    printf '%%s' '{ "reviews": [ { "runId": "%s", "verdict": "<approved|changes_requested>", "body": "<review plus publicationError: invalid GitLab MR URL>" } ] }' | ao review submit --session %s --reviews -
`, reviewQueueText(spec), spec.RunID, spec.WorkerID)
	}
	endpoint := fmt.Sprintf("projects/%s/merge_requests/%d", url.PathEscape(ref.Repository), ref.Number)
	glab := fmt.Sprintf(`For the existing project-supported glab route, use these exact target-bound command shapes. Substitute JSON values only; do not use current-directory placeholders such as :id or :fullpath, a different host or project, a full API URL, custom auth headers, or flags that retry writes. Keep each printf JSON body on one line and shell-escape single quotes. Existing project credentials/configuration stay unchanged.

    glab api --hostname %s --method GET %s
    glab api --hostname %s --method GET %s --paginate
    glab api --hostname %s --method GET %s --paginate
    glab api --hostname %s --method GET %s --paginate

    printf '%%s' '{ "note": "<required finding plus run/head/finding marker>", "position": { "position_type": "text", "base_sha": "<base>", "head_sha": "<reviewed head>", "start_sha": "<start>", "old_path": "<old path>", "new_path": "<new path>", "new_line": 1 }, "resolve_discussion": false }' | glab api --hostname %s --method POST %s --header 'Content-Type: application/json' --input - --include

    printf '%%s' '{ "note": "<summary plus run/head marker>", "reviewer_state": "<reviewed|requested_changes>" }' | glab api --hostname %s --method POST %s --header 'Content-Type: application/json' --input - --include

Use the actual changed line instead of the example new_line=1; for a deletion replace new_line with old_line. --paginate must finish every page; older glab versions may print separate JSON arrays, so inspect all pages. A bulk publish has no review ID: an empty body with exit 0 and a confirmed HTTP 2xx response is an acknowledgement, not a missing draft ID. Read back the summary and complete discussions before claiming publication. Only GETs may be repeated for reconciliation. Never repeat POST, even through the other route.
`, shellQuote(ref.Host), shellQuote(endpoint),
		shellQuote(ref.Host), shellQuote(endpoint+"/draft_notes?per_page=100"),
		shellQuote(ref.Host), shellQuote(endpoint+"/notes?per_page=100"),
		shellQuote(ref.Host), shellQuote(endpoint+"/discussions?per_page=100"),
		shellQuote(ref.Host), shellQuote(endpoint+"/draft_notes"),
		shellQuote(ref.Host), shellQuote(endpoint+"/draft_notes/bulk_publish"))

	return fmt.Sprintf(`Review the GitLab merge request owned by worker session %s.
%s
Publish directly from this reviewer using the project's existing GitLab MCP OR glab route. Prefer suitable MCP operations when the project exposes them; otherwise use its already configured glab route. Select one route and account before the first write, then keep it for this attempt. AO's daemon only records your local result; it does not publish it. Never use GitHub commands for this GitLab task.

1. For MCP, use the actual exposed tool names and schemas for gitlab_mr_get, gitlab_mr_draft_note_list, gitlab_mr_draft_note_create, gitlab_mr_draft_note_publish_all, gitlab_mr_notes_list and gitlab_mr_discussion_list on the server for this project. MCP absence alone does not block the existing glab route. Do not require one server to access every queued project. If neither project-supported route is available, this harness cannot execute it, or inherited permissions deny it, record the exact missing operation or permission in publicationError. Continue the local review and AO bookkeeping; do not change configuration or approvals, bypass managed policy, install a CLI or use a host publication action. Do not route around a permission denial.

2. Bind this run to the exact queue URL, full project path (including every subgroup), MR IID and reviewed head. Use that URL's host, including self-hosted GitLab. For MCP, pass the full URL-encoded project path or a project ID verified against that host and path. Get the MR, verify its returned web_url and IID match the requested URL and MR IID, require sha and diff_refs.head_sha to equal the queued target, and retain diff_refs.base_sha/head_sha/start_sha. A host/project/IID mismatch, missing diff refs or changed head blocks every write for this run.

3. Use a stable marker in the summary: <!-- ao-review run=%s head=%s -->. Give each required finding the same marker plus a distinct finding number. First read all pages of pending drafts AND published notes/discussions. A marker from an earlier attempt requires read-only reconciliation, never another POST. Existing drafts not created and acknowledged in this current attempt block publication, including unrelated drafts by the same account. Do not publish, delete, edit or resolve those drafts.

4. Every required change, including a design-level finding, must be a positioned unresolved diff discussion, not an ordinary note or summary alone. Stage it with gitlab_mr_draft_note_create or the equivalent glab draft_notes POST using position_type=text, base_sha/head_sha/start_sha from this MR, exact old_path/new_path and a positive new_line for additions or old_line for deletions; set resolve_discussion=false. Optional suggestions stay in the summary. Immediately before EACH write, get the MR again and require the same reviewed head. Capture each returned draft ID, body and position. Read all draft pages back and require exactly the acknowledged draft IDs, markers, bodies and positions from this attempt; any missing, changed or additional draft blocks the bulk write. Do not individually publish drafts.

5. Recheck the head and exact draft batch immediately before ONE gitlab_mr_draft_note_publish_all or equivalent glab bulk_publish POST. Include the full review summary and marker in note, and reviewer_state=reviewed for an approved local verdict or requested_changes for changes_requested. With no findings, still list drafts, require the empty batch and publish the summary this way. These reviewer states are not formal GitLab approvals. Never call approval or merge APIs.

6. A timeout, transport error, HTTP 5xx, undecodable response where JSON is required, missing draft ID or missing success confirmation is an UNCERTAIN publication outcome: the write may already have happened. Stop writes for this run. Do not repeat a POST or switch tools, even if a read currently shows nothing. Reconcile READ-ONLY with the same run/head marker: list all pending drafts, notes and discussions, and compare the findings and positions. GitLab can normalize note bodies when publishing, so preserve the stable markers and verify any normalized body still represents the exact finding. After an acknowledged bulk call, also read back the summary and every required discussion. Claim published only when that evidence confirms this run's complete batch and summary on the reviewed head. Otherwise record publicationError and status unconfirmed, preserving known draft/discussion IDs and errors. An empty draft list alone does not prove publication or failure.

7. Verify required discussions are non-individual, resolvable, unresolved and positioned before claiming findings can reach the worker. AO's existing SCM observer forwards these inline findings when review auto-inject is enabled. A summary alone does not reach the worker. Provider readback is not proof of observer polling or worker receipt; keep local verdict, provider publication and worker delivery status separate. Do not claim worker receipt without evidence.

%s
Record this run's local AO verdict and full review body even if publication was blocked or remains uncertain. Include a separate provider publication section with route, published/blocked/unconfirmed, exact target/head, marker, confirmed note/discussion IDs or publicationError; report worker delivery as unconfirmed unless observed. Leave githubReviewId empty for GitLab; no GitLab note, discussion, draft, MR or run ID belongs in it. AO approved means a local verdict, never formal approval.

Pass JSON on stdin, on one line, shell-escape single quotes and do not use a heredoc or write files into the checkout:

    printf '%%s' '{ "reviews": [ { "runId": "%s", "verdict": "<approved|changes_requested>", "body": "<full review plus separate provider status and worker delivery status>" } ] }' | ao review submit --session %s --reviews -
`, spec.WorkerID, reviewQueueText(spec), spec.RunID, spec.TargetSHA, glab, spec.RunID, spec.WorkerID)
}
