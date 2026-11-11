-- The schema as localtickets.Open built it before migrations (PR #91).
-- IF NOT EXISTS lets the databases it built adopt this file unchanged.
-- +goose Up
CREATE TABLE IF NOT EXISTS tickets (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title      TEXT NOT NULL,
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	closed_at  TEXT
);
CREATE TABLE IF NOT EXISTS comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id  INTEGER NOT NULL REFERENCES tickets(id),
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE comments;
DROP TABLE tickets;
