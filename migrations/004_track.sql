CREATE TABLE IF NOT EXISTS track (
	video_id varchar(20) PRIMARY KEY,
	url varchar(255) NOT NULL,
	title varchar(300) NOT NULL,
	author varchar(200) NOT NULL DEFAULT '',
	duration_seconds int NOT NULL DEFAULT 0,
	thumbnail varchar(500) NOT NULL DEFAULT '',
	added_by varchar(100) NOT NULL DEFAULT '',
	first_added_at timestamptz NOT NULL DEFAULT now(),
	last_added_at timestamptz NOT NULL DEFAULT now(),
	times_added int NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS track_last_added_at_idx ON track (last_added_at DESC);
