-- name: UpsertPRComment :exec
INSERT INTO pr_comment (pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, review_id, url, is_bot, auto_inject_review)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (pr_url, comment_id) DO UPDATE SET
    author = excluded.author,
    file = excluded.file,
    line = excluded.line,
    body = excluded.body,
    resolved = excluded.resolved,
    created_at = excluded.created_at,
    thread_id = excluded.thread_id,
    review_id = excluded.review_id,
    url = excluded.url,
    is_bot = excluded.is_bot;

-- name: InsertLegacyPRComment :exec
INSERT OR IGNORE INTO pr_comment (pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, review_id, url, is_bot)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteLegacyPRComments :exec
DELETE FROM pr_comment WHERE pr_url = ? AND thread_id = '';

-- name: DeletePRComment :exec
DELETE FROM pr_comment WHERE pr_url = ? AND comment_id = ?;

-- name: MarkPRCommentResolved :execrows
UPDATE pr_comment SET resolved = TRUE WHERE pr_url = ? AND comment_id = ?;

-- name: MarkPRCommentsResolvedForThread :execrows
UPDATE pr_comment
SET resolved = TRUE
WHERE pr_url = ? AND thread_id = ? AND resolved = FALSE;

-- name: ListPRComments :many
SELECT pr_url, comment_id, author, file, line, body, resolved, created_at, thread_id, url, is_bot,
    COALESCE((
        SELECT review_run.auto_inject_review FROM review_run
        WHERE review_run.pr_url = pr_comment.pr_url
                AND json_extract(review_run.publication_json, '$.provider') = 'gitlab'
                AND json_extract(review_run.publication_json, '$.status') = 'published'
                AND json_extract(review_run.publication_json, '$.targetSha') = review_run.target_sha
                AND EXISTS (
                    SELECT 1 FROM json_each(review_run.publication_json, '$.refs') AS ref
                    WHERE json_extract(ref.value, '$.kind') = 'note'
                      AND json_extract(ref.value, '$.id') = pr_comment.comment_id
                )
        ORDER BY review_run.created_at DESC, review_run.id DESC LIMIT 1
    ), pr_comment.auto_inject_review) AS auto_inject_review,
    review_id
FROM pr_comment WHERE pr_comment.pr_url = ? ORDER BY pr_comment.created_at, pr_comment.comment_id;
