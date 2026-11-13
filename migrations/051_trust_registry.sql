CREATE TABLE IF NOT EXISTS trust_publishers (
	id bigserial PRIMARY KEY,
	slug text NOT NULL UNIQUE,
	name text NOT NULL,
	trust_level text NOT NULL DEFAULT 'unknown',
	homepage text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS trust_products (
	id bigserial PRIMARY KEY,
	publisher_id bigint NOT NULL REFERENCES trust_publishers(id) ON DELETE CASCADE,
	slug text NOT NULL,
	name text NOT NULL,
	platform text NOT NULL,
	package_name text NOT NULL DEFAULT '',
	kind text NOT NULL DEFAULT 'app',
	created_at timestamptz NOT NULL DEFAULT now(),
	UNIQUE (slug, platform)
);

CREATE TABLE IF NOT EXISTS trust_sources (
	id bigserial PRIMARY KEY,
	product_id bigint NOT NULL REFERENCES trust_products(id) ON DELETE CASCADE,
	url text NOT NULL DEFAULT '',
	kind text NOT NULL DEFAULT 'official',
	trust_level text NOT NULL DEFAULT 'unknown',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS trust_releases (
	id bigserial PRIMARY KEY,
	product_id bigint NOT NULL REFERENCES trust_products(id) ON DELETE CASCADE,
	channel text NOT NULL DEFAULT 'stable',
	version text NOT NULL,
	version_code bigint NOT NULL DEFAULT 0,
	package_name text NOT NULL DEFAULT '',
	sha256 text NOT NULL,
	signature_sha256 text NOT NULL DEFAULT '',
	signature_type text NOT NULL DEFAULT '',
	file_size bigint NOT NULL DEFAULT 0,
	url text NOT NULL DEFAULT '',
	published_at timestamptz NOT NULL DEFAULT now(),
	created_at timestamptz NOT NULL DEFAULT now(),
	UNIQUE (product_id, channel, version, sha256)
);

CREATE INDEX IF NOT EXISTS trust_releases_lookup_idx
	ON trust_releases (product_id, channel, published_at DESC);

INSERT INTO trust_publishers (slug, name, trust_level, homepage)
VALUES ('streamhata', 'StreamHata', 'official', 'https://streamhata.rdpctd.dev')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
SELECT p.id, 'streamhata', 'StreamHata', 'android', 'dev.rdpctd.streamhata.android', 'app'
FROM trust_publishers p WHERE p.slug = 'streamhata'
ON CONFLICT (slug, platform) DO NOTHING;

INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
SELECT p.id, 'streamhata-chat', 'StreamHata Chat', 'android', 'dev.rdpctd.streamhata.chat', 'app'
FROM trust_publishers p WHERE p.slug = 'streamhata'
ON CONFLICT (slug, platform) DO NOTHING;

INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
SELECT p.id, 'streamhata-desktop', 'StreamHata Desktop', 'windows', '', 'app'
FROM trust_publishers p WHERE p.slug = 'streamhata'
ON CONFLICT (slug, platform) DO NOTHING;

INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
SELECT p.id, 'streamhata-desktop', 'StreamHata Desktop', 'linux', '', 'app'
FROM trust_publishers p WHERE p.slug = 'streamhata'
ON CONFLICT (slug, platform) DO NOTHING;

INSERT INTO trust_products (publisher_id, slug, name, platform, package_name, kind)
SELECT p.id, 'streamhata-desktop', 'StreamHata Desktop', 'macos', '', 'app'
FROM trust_publishers p WHERE p.slug = 'streamhata'
ON CONFLICT (slug, platform) DO NOTHING;
