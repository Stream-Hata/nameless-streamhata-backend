ALTER TABLE users ALTER COLUMN updated_at SET DEFAULT now();
UPDATE users SET updated_at = now() WHERE updated_at IS NULL;
ALTER TABLE users ALTER COLUMN updated_at SET NOT NULL;
