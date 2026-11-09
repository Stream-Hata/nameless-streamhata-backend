CREATE TABLE IF NOT EXISTS blog_articles (
	id bigserial PRIMARY KEY,
	slug text NOT NULL UNIQUE,
	title text NOT NULL,
	excerpt text NOT NULL DEFAULT '',
	content text NOT NULL DEFAULT '',
	cover_url text NOT NULL DEFAULT '',
	status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
	author_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
	author_login text NOT NULL,
	author_name text NOT NULL DEFAULT '',
	published_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS blog_articles_published_idx
	ON blog_articles (status, published_at DESC);
