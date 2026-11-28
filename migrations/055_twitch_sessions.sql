CREATE TABLE twitch_session (
    digest bytea PRIMARY KEY,
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX twitch_session_expires_idx ON twitch_session (expires_at);
