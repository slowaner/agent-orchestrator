# ao review

Manage AO's native code reviewer for a worker's PR.

AO's reviewer is an isolated reviewer agent for implementation PRs owned by an
AO worker. To review that worker's PR, use `ao review trigger`. Inside a worker,
the review commands default to the calling session (`AO_SESSION_ID`), so
`ao review trigger` reviews your own implementation PR. An orchestrator passes
the implementation worker's session id.

For a requested review of someone else's PR, spawn or use a normal review-only
worker without claiming or attaching the target PR. That worker submits the
requested provider review and comments directly. Do not run `ao review trigger`,
create an ownership-only placeholder, or require an implementation owner just
to review the PR.

AO's review is internal. An AO approval is not a formal GitHub or GitLab approval: it does not
satisfy required or independent-account reviews or branch protection, and it
does not authorize merging.

## Syntax

```
ao review <subcommand> [args] [flags]
```

The review loop can be inspected, submitted, cancelled, or triggered again.

## Subcommands

---

### ao review ls

List review runs for a worker session (default: the calling session). Alias: `list`.

**Syntax:**
```
ao review ls [worker-session-id] [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--json` | Output reviews as JSON | - |

**Example:**

```bash
ao review ls mer-3
```

---

### ao review submit

Record a reviewer's result for a worker's PR. This records AO's local verdict;
submission alone does not publish a provider review.

GitLab reviewers publish directly through the project's available GitLab MCP
operations or its already configured `glab` CLI. Suitable MCP operations are
preferred; MCP absence alone does not block the project's `glab` route. The provider-specific instructions require the exact host, full subgroup
project path, MR IID and reviewed head, and block changed heads or unrelated
pending drafts. Required findings use positioned unresolved diff discussions:
draft them with `gitlab_mr_draft_note_create`, then publish the verified batch
once with `gitlab_mr_draft_note_publish_all`, its summary and `reviewer_state`.
The equivalent `glab api` operations are GET of the MR, drafts, notes and
discussions, then POST to `draft_notes` and `draft_notes/bulk_publish`. Use the
explicit `--hostname` and URL-encoded full project path from the review task;
pass JSON through stdin with `--input -`, and paginate every listing. See the
[glab API command](https://docs.gitlab.com/cli/api/) and
[GitLab draft notes API](https://docs.gitlab.com/api/draft_notes/) contracts.
A review without findings still publishes its summary through that operation.
GitLab's `reviewed` state is not a formal approval. GitHub keeps its direct
`gh api` COMMENT review workflow, including in mixed-provider queues.

Keep local verdict, provider publication and worker delivery separate in `body`.
Leave `githubReviewId` and `--review-id` empty for new GitLab results. Select one
project-supported route and account before any write. If the harness exposes
neither route or permission is denied, report publication as blocked; do not
install another CLI, change security settings or substitute a GitHub command.
The instructions do not grant permissions or require an AO host server to access
every project.

After an uncertain write, stop POSTs and reconcile the same run/head marker
through read-only draft, note and discussion listings. Never assume a timeout,
5xx, malformed response or missing ID means nothing was published. Record an
explicit publication error or unconfirmed status until the summary and complete
batch are confirmed. The daemon only records the result; it adds no publisher or
durable provider-attempt state. Existing SCM observation forwards actionable
inline discussions when auto-inject is enabled. Summary publication alone does
not prove observer polling or worker receipt.

**Syntax:**
```
ao review submit [worker-session-id] [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--body string` | Review body: a path to a Markdown file, or `-` to read from stdin | - |
| `--review-id string` | Id of the GitHub PR review just posted (the `.id` from the `gh api` POST that created the review) | - |
| `--reviews string` | JSON review results array or object: a path, or `-` to read from stdin | - |
| `--run string` | Review run id | Required |
| `--session string` | Worker session id (or pass it as the positional argument) | - |
| `--verdict string` | Review verdict: `approved` or `changes_requested` | Required |

If the local daemon is restarting when a result is submitted, AO retains the
parsed result in memory and retries the same idempotent request for up to 30
seconds. Validation errors return immediately. If the daemon remains unavailable,
the command reports failure and can be repeated safely; daemon idempotency
handles the case where an earlier connection dropped after committing the result.

**Examples:**

```bash
# Submit an approved review for session mer-3
ao review submit mer-3 --run review-run-1 --verdict approved
```

```bash
# Submit a changes-requested review with a body from stdin
echo "Please fix the null check on line 42." | ao review submit --session mer-3 --run review-run-1 --verdict changes_requested --body -
```

---

### ao review cancel

Cancel every running review for a worker's PR (default: the calling session). Alias: `stop`.

**Syntax:**
```
ao review cancel [worker-session-id] [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--session string` | Worker session id (or pass it positionally) | - |

**Example:**

```bash
ao review cancel mer-3
```

---

### ao review trigger

Start AO's reviewer on a worker's open PR heads (default: the calling session).
Aliases: `execute`, `restart`. Reviewer panes cannot run it.

**Syntax:**
```
ao review trigger [worker-session-id] [flags]
```

**Flags:**

| Flag | Meaning | Default / Required |
|---|---|---|
| `--session string` | Worker session id (or pass it positionally) | The calling AO session |
| `--pr string` | Review only this session's implementation PR URL, attaching it to the session first if AO does not track it yet (never from another active session). Do not pass an external review target | Every eligible PR on the session |
| `--agent string` | Reviewer agent for this pass only (alias `--harness`) | Session reviewer, then project reviewer, then the project's default worker agent |
| `--model string` | Reviewer model for this pass only | As above |
| `--effort string` | Reviewer reasoning effort for this pass only | As above |
| `--rerun` | Review a head that already has a review again, or add a reviewer with a different agent alongside one that is still running | - |
| `--no-inject` | Leave the session's review auto-inject setting unchanged | Auto-inject is turned on |

AO fetches the session's PRs fresh from the provider before deciding what is
due, so the pass covers the commit really on the PR. Right after opening a PR,
pass `--pr <url>`.

A head that is already being reviewed, or already has a review, is not reviewed
again: the command exits 1 with `REVIEW_ALREADY_RUNNING` or
`REVIEW_HEAD_ALREADY_REVIEWED` and says what to do. Push new commits, or pass
`--rerun`. The trigger re-reads the PR from the provider first; if you
just pushed and it still says already reviewed, wait a few seconds and retry. The same reviewer agent never runs twice on one head at the same time.

By default the command turns on the worker session's review auto-inject. The
reviewer posts an inline provider comment for each change it requires, and AO
forwards those comments to the worker like any other review comments. An
approval adds no comments, so check the verdict with `ao review ls`. If a run
shows `failed` or `cancelled`, trigger again; nothing else reports it.

**Examples:**

```bash
# Inside a worker: request a review of this session's PR once it is ready
ao review trigger
```

```bash
# Orchestrator: review a worker's PR with a specific reviewer
ao review trigger mer-3 --agent codex --model gpt-5.5
```

```bash
# Add a second opinion on the same commit while the first reviewer runs
ao review trigger --agent claude-code --rerun
```
