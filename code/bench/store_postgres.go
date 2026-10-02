package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresSchema = `
CREATE TABLE IF NOT EXISTS bench_tasks (
	instance_id       TEXT PRIMARY KEY,
	repo              TEXT NOT NULL,
	language          TEXT NOT NULL,
	hard              BOOLEAN NOT NULL,
	issue_categories  JSONB NOT NULL DEFAULT '[]',
	issue_specificity JSONB NOT NULL DEFAULT '[]',
	gold_files        INTEGER NOT NULL,
	gold_lines        INTEGER NOT NULL,
	gold_paths        JSONB NOT NULL DEFAULT '[]',
	f2p_count         INTEGER NOT NULL,
	p2p_count         INTEGER NOT NULL,
	image             TEXT NOT NULL,
	updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS bench_runs (
	id                TEXT PRIMARY KEY,
	created_at        TIMESTAMPTZ NOT NULL,
	benchmark         TEXT NOT NULL,
	subset            TEXT NOT NULL,
	solver            TEXT NOT NULL,
	model             TEXT NOT NULL DEFAULT '',
	trials            INTEGER NOT NULL,
	concurrency       INTEGER NOT NULL,
	git_commit        TEXT NOT NULL DEFAULT '',
	git_dirty         BOOLEAN NOT NULL DEFAULT false,
	config            JSONB NOT NULL DEFAULT '{}',
	tasks_repo_commit TEXT NOT NULL,
	dataset_revision  TEXT NOT NULL DEFAULT '',
	host              JSONB NOT NULL DEFAULT '{}',
	status            TEXT NOT NULL,
	started_at        TIMESTAMPTZ NOT NULL,
	finished_at       TIMESTAMPTZ,
	notes             TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS bench_runs_history ON bench_runs (benchmark, subset, solver, model, created_at);

CREATE TABLE IF NOT EXISTS bench_attempts (
	run_id              TEXT NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
	instance_id         TEXT NOT NULL,
	trial               INTEGER NOT NULL,
	phase               TEXT NOT NULL,
	resolved            BOOLEAN NOT NULL DEFAULT false,
	f2p_passed          INTEGER NOT NULL DEFAULT 0,
	f2p_total           INTEGER NOT NULL DEFAULT 0,
	p2p_passed          INTEGER NOT NULL DEFAULT 0,
	p2p_total           INTEGER NOT NULL DEFAULT 0,
	failure_category    TEXT NOT NULL DEFAULT '',
	error               TEXT NOT NULL DEFAULT '',
	patch               TEXT NOT NULL DEFAULT '',
	patch_files         INTEGER NOT NULL DEFAULT 0,
	patch_added         INTEGER NOT NULL DEFAULT 0,
	patch_removed       INTEGER NOT NULL DEFAULT 0,
	touches_tests       BOOLEAN NOT NULL DEFAULT false,
	gold_file_precision DOUBLE PRECISION,
	gold_file_recall    DOUBLE PRECISION,
	steps               INTEGER NOT NULL DEFAULT 0,
	llm_calls           INTEGER NOT NULL DEFAULT 0,
	tool_calls          JSONB NOT NULL DEFAULT '{}',
	execute_calls       INTEGER NOT NULL DEFAULT 0,
	test_runs           INTEGER NOT NULL DEFAULT 0,
	files_read          INTEGER NOT NULL DEFAULT 0,
	first_edit_step     INTEGER,
	summarizations      INTEGER NOT NULL DEFAULT 0,
	peak_context_tokens INTEGER NOT NULL DEFAULT 0,
	tokens_in           BIGINT NOT NULL DEFAULT 0,
	tokens_out          BIGINT NOT NULL DEFAULT 0,
	cache_read          BIGINT NOT NULL DEFAULT 0,
	cache_write         BIGINT NOT NULL DEFAULT 0,
	cost_usd            DOUBLE PRECISION,
	agent_ms            BIGINT NOT NULL DEFAULT 0,
	model_ms            BIGINT NOT NULL DEFAULT 0,
	tool_ms             BIGINT NOT NULL DEFAULT 0,
	execute_ms          BIGINT NOT NULL DEFAULT 0,
	grade_ms            BIGINT NOT NULL DEFAULT 0,
	image_digest        TEXT NOT NULL DEFAULT '',
	trace_path          TEXT NOT NULL DEFAULT '',
	log_dir             TEXT NOT NULL DEFAULT '',
	started_at          TIMESTAMPTZ NOT NULL,
	finished_at         TIMESTAMPTZ,
	PRIMARY KEY (run_id, instance_id, trial)
);
ALTER TABLE bench_attempts ADD COLUMN IF NOT EXISTS subagent_llm_calls INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS bench_test_results (
	run_id      TEXT NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
	instance_id TEXT NOT NULL,
	trial       INTEGER NOT NULL,
	test_name   TEXT NOT NULL,
	kind        TEXT NOT NULL,
	status      TEXT NOT NULL,
	PRIMARY KEY (run_id, instance_id, trial, kind, test_name)
);

CREATE TABLE IF NOT EXISTS bench_references (
	source      TEXT NOT NULL,
	harness     TEXT NOT NULL,
	model       TEXT NOT NULL,
	instance_id TEXT NOT NULL,
	resolved    BOOLEAN NOT NULL,
	steps       INTEGER,
	cost_usd    DOUBLE PRECISION,
	url         TEXT NOT NULL DEFAULT '',
	imported_at TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (harness, model, instance_id)
);

-- Scored attempts: infra errors are retried, not counted. a.* is fixed when
-- the view is created, so a new attempt column needs the views rebuilt.
DROP VIEW IF EXISTS bench_scored_attempts CASCADE;
CREATE VIEW bench_scored_attempts AS
SELECT a.*, t.repo, t.language, t.hard, t.issue_categories, t.gold_files, t.gold_lines
FROM bench_attempts a
LEFT JOIN bench_tasks t USING (instance_id)
WHERE a.phase = 'done' AND a.failure_category <> 'infra_error';

CREATE OR REPLACE VIEW bench_run_summary AS
SELECT r.id AS run_id, r.created_at, r.benchmark, r.subset, r.solver, r.model, r.git_commit, r.git_dirty, r.status,
	count(a.*) AS attempts,
	count(a.*) FILTER (WHERE a.resolved) AS resolved,
	CASE WHEN count(a.*) > 0 THEN (count(a.*) FILTER (WHERE a.resolved))::float / count(a.*) END AS resolve_rate,
	sum(a.cost_usd) AS cost_usd,
	percentile_cont(0.5) WITHIN GROUP (ORDER BY a.steps) AS median_steps,
	percentile_cont(0.5) WITHIN GROUP (ORDER BY a.agent_ms) AS median_agent_ms,
	percentile_cont(0.5) WITHIN GROUP (ORDER BY a.tokens_in) AS median_tokens_in,
	percentile_cont(0.5) WITHIN GROUP (ORDER BY a.tokens_out) AS median_tokens_out,
	avg(a.gold_file_recall) AS mean_gold_recall,
	avg(a.gold_file_precision) AS mean_gold_precision
FROM bench_runs r
LEFT JOIN bench_scored_attempts a ON a.run_id = r.id
GROUP BY r.id;

CREATE OR REPLACE VIEW bench_run_breakdown AS
SELECT run_id, 'language' AS dimension, language AS value, count(*) AS attempts,
	count(*) FILTER (WHERE resolved) AS resolved FROM bench_scored_attempts GROUP BY run_id, language
UNION ALL
SELECT run_id, 'repo', repo, count(*), count(*) FILTER (WHERE resolved) FROM bench_scored_attempts GROUP BY run_id, repo
UNION ALL
SELECT run_id, 'hard', hard::text, count(*), count(*) FILTER (WHERE resolved) FROM bench_scored_attempts GROUP BY run_id, hard
UNION ALL
SELECT run_id, 'failure_category', failure_category, count(*), count(*) FILTER (WHERE resolved)
	FROM bench_scored_attempts GROUP BY run_id, failure_category
UNION ALL
SELECT a.run_id, 'issue_category', c.value, count(*), count(*) FILTER (WHERE a.resolved)
	FROM bench_scored_attempts a, jsonb_array_elements_text(a.issue_categories) AS c(value) GROUP BY a.run_id, c.value;

CREATE OR REPLACE VIEW bench_history AS
SELECT benchmark, subset, solver, model, run_id, created_at, git_commit, git_dirty, attempts, resolved, resolve_rate,
	cost_usd, median_steps, median_agent_ms
FROM bench_run_summary
WHERE status IN ('completed', 'partial')
ORDER BY benchmark, subset, solver, model, created_at;

CREATE OR REPLACE VIEW bench_reference_on_subset AS
SELECT r.id AS run_id, ref.harness, ref.model,
	count(DISTINCT ref.instance_id) AS tasks,
	count(DISTINCT ref.instance_id) FILTER (WHERE ref.resolved) AS resolved
FROM bench_runs r
JOIN bench_attempts a ON a.run_id = r.id
JOIN bench_references ref ON ref.instance_id = a.instance_id
GROUP BY r.id, ref.harness, ref.model;
`

// PostgresStore keeps bench data in the bench_* tables.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates tables and views if needed. The caller owns pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("bench.NewPostgresStore: nil pool")
	}
	if _, err := pool.Exec(ctx, postgresSchema); err != nil {
		return nil, fmt.Errorf("bench: create schema: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func jsonOrEmpty(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}

func rawOrEmpty(r json.RawMessage) []byte {
	if len(r) == 0 {
		return []byte("{}")
	}
	return r
}

func (s *PostgresStore) UpsertTasks(ctx context.Context, tasks []Task) error {
	batch := &pgx.Batch{}
	for _, t := range tasks {
		batch.Queue(`
INSERT INTO bench_tasks (instance_id, repo, language, hard, issue_categories, issue_specificity,
	gold_files, gold_lines, gold_paths, f2p_count, p2p_count, image, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
ON CONFLICT (instance_id) DO UPDATE SET repo=EXCLUDED.repo, language=EXCLUDED.language, hard=EXCLUDED.hard,
	issue_categories=EXCLUDED.issue_categories, issue_specificity=EXCLUDED.issue_specificity,
	gold_files=EXCLUDED.gold_files, gold_lines=EXCLUDED.gold_lines, gold_paths=EXCLUDED.gold_paths,
	f2p_count=EXCLUDED.f2p_count, p2p_count=EXCLUDED.p2p_count, image=EXCLUDED.image, updated_at=now()`,
			t.InstanceID, t.Repo, t.Language, t.Hard, jsonOrEmpty(t.IssueCategories, "[]"),
			jsonOrEmpty(t.IssueSpecificity, "[]"), len(t.GoldPaths), t.GoldAdded+t.GoldRemoved,
			jsonOrEmpty(t.GoldPaths, "[]"), len(t.FailToPass), len(t.PassToPass), t.Image)
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("bench: upsert tasks: %w", err)
	}
	return nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, r *Run) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO bench_runs (id, created_at, benchmark, subset, solver, model, trials, concurrency, git_commit, git_dirty,
	config, tasks_repo_commit, dataset_revision, host, status, started_at, notes)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status, concurrency = EXCLUDED.concurrency, finished_at = NULL`,
		r.ID, r.CreatedAt, r.Benchmark, r.Subset, r.Solver, r.Model, r.Trials, r.Concurrency, r.GitCommit, r.GitDirty,
		rawOrEmpty(r.Config), r.TasksRepoCommit, r.DatasetRevision, rawOrEmpty(r.Host), r.Status, r.StartedAt, r.Notes)
	if err != nil {
		return fmt.Errorf("bench: create run %s: %w", r.ID, err)
	}
	return nil
}

func (s *PostgresStore) FinishRun(ctx context.Context, id, status string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE bench_runs SET status=$2, finished_at=$3 WHERE id=$1`, id, status, at)
	if err != nil {
		return fmt.Errorf("bench: finish run %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

const runColumns = `id, created_at, benchmark, subset, solver, model, trials, concurrency, git_commit, git_dirty,
	config, tasks_repo_commit, dataset_revision, host, status, started_at, finished_at, notes`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var cfg, host []byte
	err := row.Scan(&r.ID, &r.CreatedAt, &r.Benchmark, &r.Subset, &r.Solver, &r.Model, &r.Trials, &r.Concurrency,
		&r.GitCommit, &r.GitDirty, &cfg, &r.TasksRepoCommit, &r.DatasetRevision, &host, &r.Status, &r.StartedAt,
		&r.FinishedAt, &r.Notes)
	r.Config, r.Host = cfg, host
	return r, err
}

func (s *PostgresStore) Run(ctx context.Context, id string) (*Run, error) {
	r, err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM bench_runs WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("bench: run %s: %w", id, err)
	}
	return &r, nil
}

func (s *PostgresStore) Runs(ctx context.Context, f RunFilter) ([]Run, error) {
	var where []string
	var args []any
	add := func(col, v string) {
		if v != "" {
			args = append(args, v)
			where = append(where, fmt.Sprintf("%s = $%d", col, len(args)))
		}
	}
	add("subset", f.Subset)
	add("model", f.Model)
	add("solver", f.Solver)
	q := `SELECT ` + runColumns + ` FROM bench_runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at DESC"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("bench: runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("bench: runs: %w", err)
		}
		out = append([]Run{r}, out...)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SaveAttempt(ctx context.Context, a *Attempt) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO bench_attempts (run_id, instance_id, trial, phase, resolved, f2p_passed, f2p_total, p2p_passed, p2p_total,
	failure_category, error, patch, patch_files, patch_added, patch_removed, touches_tests, gold_file_precision,
	gold_file_recall, steps, llm_calls, tool_calls, execute_calls, test_runs, files_read, first_edit_step, summarizations,
	peak_context_tokens, tokens_in, tokens_out, cache_read, cache_write, cost_usd, agent_ms, model_ms, tool_ms,
	execute_ms, grade_ms, image_digest, trace_path, log_dir, started_at, finished_at, subagent_llm_calls)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,
	$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43)
ON CONFLICT (run_id, instance_id, trial) DO UPDATE SET phase=EXCLUDED.phase, resolved=EXCLUDED.resolved,
	f2p_passed=EXCLUDED.f2p_passed, f2p_total=EXCLUDED.f2p_total, p2p_passed=EXCLUDED.p2p_passed,
	p2p_total=EXCLUDED.p2p_total, failure_category=EXCLUDED.failure_category, error=EXCLUDED.error,
	patch=EXCLUDED.patch, patch_files=EXCLUDED.patch_files, patch_added=EXCLUDED.patch_added,
	patch_removed=EXCLUDED.patch_removed, touches_tests=EXCLUDED.touches_tests,
	gold_file_precision=EXCLUDED.gold_file_precision, gold_file_recall=EXCLUDED.gold_file_recall,
	steps=EXCLUDED.steps, llm_calls=EXCLUDED.llm_calls, tool_calls=EXCLUDED.tool_calls,
	execute_calls=EXCLUDED.execute_calls, test_runs=EXCLUDED.test_runs, files_read=EXCLUDED.files_read,
	first_edit_step=EXCLUDED.first_edit_step, summarizations=EXCLUDED.summarizations,
	peak_context_tokens=EXCLUDED.peak_context_tokens, tokens_in=EXCLUDED.tokens_in, tokens_out=EXCLUDED.tokens_out,
	cache_read=EXCLUDED.cache_read, cache_write=EXCLUDED.cache_write, cost_usd=EXCLUDED.cost_usd,
	agent_ms=EXCLUDED.agent_ms, model_ms=EXCLUDED.model_ms, tool_ms=EXCLUDED.tool_ms, execute_ms=EXCLUDED.execute_ms,
	grade_ms=EXCLUDED.grade_ms, image_digest=EXCLUDED.image_digest, trace_path=EXCLUDED.trace_path,
	log_dir=EXCLUDED.log_dir, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at,
	subagent_llm_calls=EXCLUDED.subagent_llm_calls`,
		a.RunID, a.InstanceID, a.Trial, a.Phase, a.Resolved, a.F2PPassed, a.F2PTotal, a.P2PPassed, a.P2PTotal,
		a.FailureCategory, a.Error, a.Patch, a.PatchFiles, a.PatchAdded, a.PatchRemoved, a.TouchesTests,
		a.GoldPrecision, a.GoldRecall, a.Steps, a.LLMCalls, jsonOrEmpty(a.ToolCalls, "{}"), a.ExecuteCalls,
		a.TestRuns, a.FilesRead, a.FirstEditStep, a.Summarizations, a.PeakContextTokens, a.TokensIn, a.TokensOut,
		a.CacheRead, a.CacheWrite, a.CostUSD, a.AgentMS, a.ModelMS, a.ToolMS, a.ExecuteMS, a.GradeMS,
		a.ImageDigest, a.TracePath, a.LogDir, a.StartedAt, a.FinishedAt, a.SubagentLLMCalls)
	if err != nil {
		return fmt.Errorf("bench: save attempt %s/%s#%d: %w", a.RunID, a.InstanceID, a.Trial, err)
	}
	return nil
}

func (s *PostgresStore) Attempts(ctx context.Context, runID string) ([]Attempt, error) {
	rows, err := s.pool.Query(ctx, `
SELECT run_id, instance_id, trial, phase, resolved, f2p_passed, f2p_total, p2p_passed, p2p_total, failure_category,
	error, patch, patch_files, patch_added, patch_removed, touches_tests, gold_file_precision, gold_file_recall, steps,
	llm_calls, tool_calls, execute_calls, test_runs, files_read, first_edit_step, summarizations, peak_context_tokens,
	tokens_in, tokens_out, cache_read, cache_write, cost_usd, agent_ms, model_ms, tool_ms, execute_ms, grade_ms,
	image_digest, trace_path, log_dir, started_at, finished_at, subagent_llm_calls
FROM bench_attempts WHERE run_id=$1 ORDER BY instance_id, trial`, runID)
	if err != nil {
		return nil, fmt.Errorf("bench: attempts %s: %w", runID, err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var tools []byte
		if err := rows.Scan(&a.RunID, &a.InstanceID, &a.Trial, &a.Phase, &a.Resolved, &a.F2PPassed, &a.F2PTotal,
			&a.P2PPassed, &a.P2PTotal, &a.FailureCategory, &a.Error, &a.Patch, &a.PatchFiles, &a.PatchAdded,
			&a.PatchRemoved, &a.TouchesTests, &a.GoldPrecision, &a.GoldRecall, &a.Steps, &a.LLMCalls, &tools,
			&a.ExecuteCalls, &a.TestRuns, &a.FilesRead, &a.FirstEditStep, &a.Summarizations, &a.PeakContextTokens,
			&a.TokensIn, &a.TokensOut, &a.CacheRead, &a.CacheWrite, &a.CostUSD, &a.AgentMS, &a.ModelMS, &a.ToolMS,
			&a.ExecuteMS, &a.GradeMS, &a.ImageDigest, &a.TracePath, &a.LogDir, &a.StartedAt, &a.FinishedAt,
			&a.SubagentLLMCalls); err != nil {
			return nil, fmt.Errorf("bench: attempts %s: %w", runID, err)
		}
		if err := json.Unmarshal(tools, &a.ToolCalls); err != nil {
			return nil, fmt.Errorf("bench: attempts %s tool_calls: %w", runID, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) SaveTestResults(ctx context.Context, runID, instanceID string, trial int, results []TestResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bench: save test results: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM bench_test_results WHERE run_id=$1 AND instance_id=$2 AND trial=$3`,
		runID, instanceID, trial); err != nil {
		return fmt.Errorf("bench: save test results: %w", err)
	}
	rows := make([][]any, len(results))
	for i, r := range results {
		rows[i] = []any{runID, instanceID, trial, r.Name, r.Kind, r.Status}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"bench_test_results"},
		[]string{"run_id", "instance_id", "trial", "test_name", "kind", "status"}, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("bench: save test results: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) TestResults(ctx context.Context, runID string) (map[AttemptKey][]TestResult, error) {
	rows, err := s.pool.Query(ctx, `
SELECT instance_id, trial, test_name, kind, status FROM bench_test_results WHERE run_id=$1
ORDER BY instance_id, trial, kind, test_name`, runID)
	if err != nil {
		return nil, fmt.Errorf("bench: test results %s: %w", runID, err)
	}
	defer rows.Close()
	out := map[AttemptKey][]TestResult{}
	for rows.Next() {
		var k AttemptKey
		var r TestResult
		if err := rows.Scan(&k.InstanceID, &k.Trial, &r.Name, &r.Kind, &r.Status); err != nil {
			return nil, fmt.Errorf("bench: test results %s: %w", runID, err)
		}
		out[k] = append(out[k], r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ImportReferences(ctx context.Context, refs []Reference) (int, error) {
	batch := &pgx.Batch{}
	for _, r := range refs {
		at := r.ImportedAt
		if at.IsZero() {
			at = time.Now()
		}
		batch.Queue(`
INSERT INTO bench_references (source, harness, model, instance_id, resolved, steps, cost_usd, url, imported_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (harness, model, instance_id) DO UPDATE SET source=EXCLUDED.source, resolved=EXCLUDED.resolved,
	steps=EXCLUDED.steps, cost_usd=EXCLUDED.cost_usd, url=EXCLUDED.url, imported_at=EXCLUDED.imported_at`,
			r.Source, r.Harness, r.Model, r.InstanceID, r.Resolved, r.Steps, r.CostUSD, r.URL, at)
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return 0, fmt.Errorf("bench: import references: %w", err)
	}
	return len(refs), nil
}

func (s *PostgresStore) References(ctx context.Context, harness, model string) ([]Reference, error) {
	rows, err := s.pool.Query(ctx, `
SELECT source, harness, model, instance_id, resolved, steps, cost_usd, url, imported_at
FROM bench_references WHERE harness=$1 AND model=$2 ORDER BY instance_id`, harness, model)
	if err != nil {
		return nil, fmt.Errorf("bench: references: %w", err)
	}
	defer rows.Close()
	var out []Reference
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.Source, &r.Harness, &r.Model, &r.InstanceID, &r.Resolved, &r.Steps, &r.CostUSD,
			&r.URL, &r.ImportedAt); err != nil {
			return nil, fmt.Errorf("bench: references: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ReferenceSets(ctx context.Context) ([][2]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT harness, model FROM bench_references ORDER BY harness, model`)
	if err != nil {
		return nil, fmt.Errorf("bench: reference sets: %w", err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return nil, fmt.Errorf("bench: reference sets: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
