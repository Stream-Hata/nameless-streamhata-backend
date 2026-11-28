CREATE TABLE chat_translation_reply (
    channel_id text NOT NULL,
    message_id text NOT NULL,
    claimed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, message_id)
);

CREATE INDEX chat_translation_reply_claimed_at_idx ON chat_translation_reply (claimed_at);
