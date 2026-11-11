-- PR #88's settings table (the kill switch). IF NOT EXISTS lets databases
-- that store.Open's own DDL already gave it adopt this file unchanged.
-- +goose Up
CREATE TABLE IF NOT EXISTS settings (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
