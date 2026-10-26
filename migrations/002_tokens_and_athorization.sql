CREATE TABLE IF NOT EXISTS tokens (
	id int generated always AS identity primary key,
	access_token varchar(1000) NOT NULL default ''
);

ALTER TABLE users
ADD COLUMN username VARCHAR(50) NOT NULL default '';

ALTER TABLE users
ADD COLUMN display_name VARCHAR(50) NOT NULL default '';

ALTER TABLE users
ADD COLUMN twitch_user_id decimal NOT NULL;

ALTER TABLE users
ADD COLUMN twitch_token INT references tokens (id);
