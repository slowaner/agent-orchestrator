package sqlite

import (
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestReviewRouteMigrationPreservesLegacyRowsAndCDC(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 20261102073811895)
	now := time.Unix(100, 0).UTC()
	_, err := db.ExecContext(t.Context(), `
INSERT INTO projects (id, path, registered_at, config) VALUES ('route', '/repo/route', ?, '{}');
INSERT INTO sessions (id, project_id, num, harness, activity_last_at, created_at, updated_at)
VALUES ('route-1', 'route', 1, 'codex', ?, ?, ?);
INSERT INTO review (id, session_id, project_id, harness, created_at, updated_at)
VALUES ('review-route', 'route-1', 'route', 'codex', ?, ?);
INSERT INTO review_run (id, review_id, session_id, harness, pr_url, target_sha, status, verdict, created_at)
VALUES ('run-route', 'review-route', 'route-1', 'codex', 'https://gitlab.example/g/p/-/merge_requests/1', 'head', 'running', 'none', ?);
`, now, now, now, now, now, now, now)
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	var route string
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT publication_route FROM review_run WHERE id='run-route'`).Scan(&route))
	require.Equal(t, "auto", route)
	_, err = db.ExecContext(t.Context(), `UPDATE review_run SET publication_route='glab' WHERE id='run-route'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT json_extract(payload, '$.publicationRoute') FROM change_log WHERE event_type='review_run_updated' ORDER BY seq DESC LIMIT 1`).Scan(&route))
	require.Equal(t, "glab", route)
	_, err = db.ExecContext(t.Context(), `UPDATE review_run SET publication_route='shell' WHERE id='run-route'`)
	require.Error(t, err)
	require.NoError(t, goose.DownToContext(t.Context(), db, "migrations", 20261102073811895))
	_, err = db.ExecContext(t.Context(), `UPDATE review_run SET body='legacy update' WHERE id='run-route'`)
	require.NoError(t, err)
	require.NoError(t, migrate(db))
	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT publication_route FROM review_run WHERE id='run-route'`).Scan(&route))
	require.Equal(t, "auto", route)
}
