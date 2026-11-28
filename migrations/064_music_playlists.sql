CREATE TABLE IF NOT EXISTS streamer_music_playlist (
	channel_id text PRIMARY KEY,
	source_url text NOT NULL DEFAULT '',
	title text NOT NULL DEFAULT '',
	tracks jsonb NOT NULL DEFAULT '[]'::jsonb,
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS streamer_music_favorite (
	channel_id text NOT NULL,
	video_id varchar(80) NOT NULL,
	track jsonb NOT NULL,
	added_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (channel_id, video_id)
);
