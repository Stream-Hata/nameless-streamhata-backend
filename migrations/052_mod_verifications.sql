CREATE TABLE IF NOT EXISTS mod_verifications (
	id bigserial PRIMARY KEY,
	source text NOT NULL DEFAULT 'modrinth',
	sha512 text NOT NULL,
	verdict text NOT NULL,
	project_id text NOT NULL DEFAULT '',
	project_slug text NOT NULL DEFAULT '',
	title text NOT NULL DEFAULT '',
	version_id text NOT NULL DEFAULT '',
	version_number text NOT NULL DEFAULT '',
	file_name text NOT NULL DEFAULT '',
	file_url text NOT NULL DEFAULT '',
	game_versions jsonb NOT NULL DEFAULT '[]'::jsonb,
	loaders jsonb NOT NULL DEFAULT '[]'::jsonb,
	reason text NOT NULL DEFAULT '',
	checked_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz,
	UNIQUE (source, sha512)
);

CREATE INDEX IF NOT EXISTS mod_verifications_expiry_idx
	ON mod_verifications (expires_at)
	WHERE expires_at IS NOT NULL;
