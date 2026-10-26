CREATE TABLE IF NOT EXISTS app_mode (
	id int generated always AS identity primary key,
	name varchar(4) NOT NULL unique
);

CREATE TABLE IF NOT EXISTS users (
	id int generated always AS identity primary key,
	created_at timestamp default now(),
	updated_at timestamp default NULL
);

CREATE TABLE IF NOT EXISTS app_state (
	id int generated always AS identity primary key,
	mode varchar(4) references app_mode (name)
);

CREATE TABLE IF NOT EXISTS timer (
	id int generated always AS identity primary key,
	minutes int NOT NULL default 0,
	seconds int NOT NULL default 0,
	hours int NOT NULL default 0
);

CREATE TABLE IF NOT EXISTS user_platforms (
	id int generated always AS identity primary key,
	code varchar(10) NOT NULL,
	name varchar(50) NOT NULL,
	icon varchar(50) NOT NULL,
	completed_games_count int NOT NULL default 0
);

CREATE TABLE IF NOT EXISTS user_state (
	state_id int references app_state (id),
	user_id int references users (id),
	primary key (state_id, user_id)
);

CREATE TABLE IF NOT EXISTS state_platforms (
	state_id int NOT NULL references app_state (id) ON DELETE CASCADE,
	platform_id int NOT NULL references user_platforms (id) ON DELETE CASCADE,
	primary key (state_id, platform_id)
);

CREATE TABLE IF NOT EXISTS timer_state (
	state_id int NOT NULL references app_state (id) ON DELETE CASCADE,
	timer_id int NOT NULL references timer (id) ON DELETE CASCADE,
	primary key (state_id, timer_id)
);

INSERT INTO
	app_mode (name)
VALUES
	('land'),
	('solo');
