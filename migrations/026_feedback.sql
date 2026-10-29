CREATE TABLE IF NOT EXISTS feedback_counter (
	prefix varchar(4) PRIMARY KEY,
	value int NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS feedback_reports (
	id bigserial PRIMARY KEY,
	prefix varchar(4) NOT NULL,
	number int NOT NULL,
	area varchar(20) NOT NULL,
	kind varchar(20) NOT NULL,
	title text NOT NULL,
	body text NOT NULL,
	author varchar(80) NOT NULL DEFAULT '',
	issue_url text NOT NULL DEFAULT '',
	issue_number int NOT NULL DEFAULT 0,
	status varchar(20) NOT NULL DEFAULT 'new',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS feedback_reports_created_idx ON feedback_reports (created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS feedback_reports_prefix_number_idx ON feedback_reports (prefix, number);
