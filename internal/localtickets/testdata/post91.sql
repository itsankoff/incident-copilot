-- sqlite3 .dump of a tickets database written by localtickets.Open after PR #91 (1759579): two tickets, one closed, one comment.
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE tickets (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title      TEXT NOT NULL,
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	closed_at  TEXT
);
INSERT INTO tickets VALUES(1,'orders is crash looping','RCA pending','2026-11-10T09:00:00Z',NULL);
INSERT INTO tickets VALUES(2,'Watchdog','always firing','2026-11-10T09:00:00Z','2026-11-10T09:00:00Z');
CREATE TABLE comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id  INTEGER NOT NULL REFERENCES tickets(id),
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
INSERT INTO comments VALUES(1,1,'Root cause: a bad config map','2026-11-10T09:00:00Z');
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('tickets',2);
INSERT INTO sqlite_sequence VALUES('comments',1);
COMMIT;
