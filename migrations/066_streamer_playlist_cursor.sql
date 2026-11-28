ALTER TABLE streamer_music_playlist
    ADD COLUMN IF NOT EXISTS current_index integer NOT NULL DEFAULT 0;
