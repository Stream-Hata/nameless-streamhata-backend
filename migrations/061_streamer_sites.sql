CREATE TABLE IF NOT EXISTS streamer_site (
	owner_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	slug varchar(80) NOT NULL UNIQUE,
	title varchar(160) NOT NULL DEFAULT '',
	description varchar(500) NOT NULL DEFAULT '',
	content text NOT NULL DEFAULT '',
	published boolean NOT NULL DEFAULT false,
	schedule jsonb NOT NULL DEFAULT '{"enabled":false,"daysOfWeek":[0,1,2,3,4,5,6],"startTime":"18:00","reminderMinutes":60}'::jsonb,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE streamer_site
	ADD COLUMN IF NOT EXISTS schedule jsonb NOT NULL DEFAULT '{"enabled":false,"daysOfWeek":[0,1,2,3,4,5,6],"startTime":"18:00","reminderMinutes":60}'::jsonb;
