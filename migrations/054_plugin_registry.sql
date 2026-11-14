CREATE TABLE IF NOT EXISTS plugin_registry (
	id bigserial PRIMARY KEY,
	slug text NOT NULL UNIQUE,
	name text NOT NULL,
	summary text NOT NULL DEFAULT '',
	kind text NOT NULL DEFAULT 'plugin',
	status text NOT NULL DEFAULT 'pending',
	latest_version text NOT NULL DEFAULT '',
	manifest jsonb NOT NULL DEFAULT '{}'::jsonb,
	publisher_slug text NOT NULL DEFAULT '',
	permissions jsonb NOT NULL DEFAULT '[]'::jsonb,
	artifact_url text NOT NULL DEFAULT '',
	artifact_sha256 text NOT NULL DEFAULT '',
	signature_type text NOT NULL DEFAULT '',
	signature text NOT NULL DEFAULT '',
	created_by text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	published_at timestamptz,
	CHECK (kind IN ('plugin', 'mod')),
	CHECK (status IN ('pending', 'published', 'rejected', 'blocked'))
);

CREATE INDEX IF NOT EXISTS plugin_registry_status_idx
	ON plugin_registry (status, updated_at DESC);

CREATE TABLE IF NOT EXISTS plugin_versions (
	id bigserial PRIMARY KEY,
	plugin_id bigint NOT NULL REFERENCES plugin_registry(id) ON DELETE CASCADE,
	version text NOT NULL,
	manifest jsonb NOT NULL,
	artifact_url text NOT NULL DEFAULT '',
	artifact_sha256 text NOT NULL DEFAULT '',
	artifact_size bigint NOT NULL DEFAULT 0,
	signature_type text NOT NULL DEFAULT '',
	signature text NOT NULL DEFAULT '',
	status text NOT NULL DEFAULT 'pending',
	created_by text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	published_at timestamptz,
	UNIQUE (plugin_id, version),
	CHECK (status IN ('pending', 'published', 'rejected', 'blocked'))
);

CREATE INDEX IF NOT EXISTS plugin_versions_status_idx
	ON plugin_versions (status, created_at DESC);
