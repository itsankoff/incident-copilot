-- The schema as store.Open built it before migrations: PR #43's tables plus
-- PR #85's lifecycle columns, in the order #85 added them. IF NOT EXISTS lets
-- the databases it built adopt this file unchanged (00001 has completed them).
-- +goose Up
CREATE TABLE IF NOT EXISTS incidents (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	target        TEXT,              -- JSON signal.Target, NULL when there is none
	key           TEXT NOT NULL,     -- <source instance>/<Signal name> when there is no Target
	opened_at     TEXT NOT NULL,
	ticket_ref    TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'open',
	outcome       TEXT NOT NULL DEFAULT '',
	open_since    TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL DEFAULT '',
	recurrence_of INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS members (
	incident_id     INTEGER NOT NULL REFERENCES incidents(id),
	key             TEXT NOT NULL,
	starts_at       TEXT NOT NULL,
	source_kind     TEXT NOT NULL,
	source_instance TEXT NOT NULL,
	name            TEXT NOT NULL,
	severity        TEXT NOT NULL,
	link            TEXT NOT NULL,
	first_seen      TEXT NOT NULL,
	last_seen       TEXT NOT NULL,
	end_outcome     TEXT NOT NULL DEFAULT '',
	ended_at        TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (incident_id, key, starts_at)
);

-- +goose Down
DROP TABLE members;
DROP TABLE incidents;
