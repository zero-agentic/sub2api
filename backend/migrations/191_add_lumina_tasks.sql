CREATE TABLE IF NOT EXISTS lumina_tasks (
    id                  BIGSERIAL PRIMARY KEY,
    task_id             VARCHAR(64) NOT NULL,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id          BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id            BIGINT NOT NULL REFERENCES groups(id) ON DELETE RESTRICT,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    upstream_task_id    VARCHAR(128) NOT NULL,
    task_type           VARCHAR(32) NOT NULL,
    model               VARCHAR(128) NOT NULL,
    status              VARCHAR(32) NOT NULL DEFAULT 'queued',
    request_payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    response_payload    JSONB NOT NULL DEFAULT '{}'::jsonb,
    error_code          VARCHAR(128),
    error_message       TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ,
    user_deleted_at     TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS lumina_tasks_task_id_key
    ON lumina_tasks(task_id);

CREATE UNIQUE INDEX IF NOT EXISTS lumina_tasks_account_upstream_key
    ON lumina_tasks(account_id, upstream_task_id);

CREATE INDEX IF NOT EXISTS lumina_tasks_owner_created_idx
    ON lumina_tasks(user_id, api_key_id, created_at DESC);

CREATE INDEX IF NOT EXISTS lumina_tasks_status_idx
    ON lumina_tasks(status);

CREATE INDEX IF NOT EXISTS lumina_tasks_user_deleted_idx
    ON lumina_tasks(user_deleted_at);
