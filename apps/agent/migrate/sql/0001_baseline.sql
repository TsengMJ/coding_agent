-- The schema the stores created on their own before migrations existed.
-- Every statement tolerates a database that already has it, so databases
-- from those versions upgrade through this file too.

-- runtime: langgraphgo checkpoints and the durable key-value store.
CREATE TABLE IF NOT EXISTS quill_checkpoints (
	id           TEXT PRIMARY KEY,
	execution_id TEXT NOT NULL,
	node_name    TEXT NOT NULL,
	state        JSONB NOT NULL,
	metadata     JSONB,
	timestamp    TIMESTAMPTZ NOT NULL,
	version      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_quill_checkpoints_execution_id ON quill_checkpoints (execution_id);
CREATE INDEX IF NOT EXISTS idx_quill_checkpoints_thread_version ON quill_checkpoints (execution_id, version DESC, timestamp DESC);

CREATE TABLE IF NOT EXISTS quill_store (
	prefix     TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (prefix, key)
);
CREATE INDEX IF NOT EXISTS quill_store_prefix ON quill_store (prefix text_pattern_ops);

-- sessions: the thread catalog.
CREATE TABLE IF NOT EXISTS quill_threads (
	thread_id     TEXT PRIMARY KEY,
	cwd           TEXT NOT NULL,
	title         TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL,
	updated_at    TIMESTAMPTZ NOT NULL,
	message_count INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS quill_threads_cwd_updated ON quill_threads (cwd, updated_at DESC);

-- storage: plan mode plans.
CREATE TABLE IF NOT EXISTS quill_plans (
	id         TEXT PRIMARY KEY,
	thread_id  TEXT NOT NULL,
	name       TEXT NOT NULL,
	status     TEXT NOT NULL,
	plan       JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS quill_plans_thread ON quill_plans (thread_id, updated_at DESC);

-- trace: run traces, with large lines deduplicated into blobs. Lines are
-- BYTEA rather than JSONB: tool output may hold NUL characters, which JSONB
-- rejects.
CREATE TABLE IF NOT EXISTS quill_trace_blobs (
	hash TEXT PRIMARY KEY,
	line BYTEA NOT NULL
);
CREATE TABLE IF NOT EXISTS quill_trace_records (
	id         BIGSERIAL PRIMARY KEY,
	thread_id  TEXT NOT NULL,
	type       TEXT NOT NULL,
	blob_hash  TEXT REFERENCES quill_trace_blobs (hash),
	data       BYTEA,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS quill_trace_records_thread ON quill_trace_records (thread_id, id);

-- workspace: per-thread repository clones.
CREATE TABLE IF NOT EXISTS quill_workspaces (
	id           TEXT PRIMARY KEY,
	owner        TEXT NOT NULL DEFAULT '',
	repo_url     TEXT NOT NULL,
	base_branch  TEXT NOT NULL DEFAULT '',
	image        TEXT NOT NULL,
	dir          TEXT NOT NULL,
	state        TEXT NOT NULL,
	created_at   TIMESTAMPTZ NOT NULL,
	last_used_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS quill_workspaces_owner ON quill_workspaces (owner, created_at DESC);

-- audit: who did what, and what it cost.
CREATE TABLE IF NOT EXISTS quill_audit (
	id           BIGSERIAL PRIMARY KEY,
	at           TIMESTAMPTZ NOT NULL,
	user_id      TEXT NOT NULL,
	action       TEXT NOT NULL,
	thread_id    TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL DEFAULT '',
	cost_usd     DOUBLE PRECISION NOT NULL DEFAULT 0,
	detail       JSONB
);
CREATE INDEX IF NOT EXISTS quill_audit_user_at ON quill_audit (user_id, at DESC);

-- auth: GitHub logins and session tokens.
CREATE TABLE IF NOT EXISTS quill_users (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL DEFAULT '',
	github_token  BYTEA NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_login_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS quill_sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES quill_users (id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS quill_sessions_expires ON quill_sessions (expires_at);
