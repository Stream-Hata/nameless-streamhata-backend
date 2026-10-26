ALTER TABLE tokens
ADD COLUMN refresh_token varchar(1000) NOT NULL default '';

ALTER TABLE tokens
ADD COLUMN user_id int references users (id);
