-- sqlite3 .dump of a database written by store.Open after PR #88 (1ef8d4d): two Incidents, three members, the kill switch set.
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE incidents (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	target     TEXT,              -- JSON signal.Target, NULL when there is none
	key        TEXT NOT NULL,     -- <source instance>/<Signal name> when there is no Target
	opened_at  TEXT NOT NULL,
	ticket_ref TEXT NOT NULL DEFAULT ''
, state TEXT NOT NULL DEFAULT 'open', outcome TEXT NOT NULL DEFAULT '', open_since TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL DEFAULT '', recurrence_of INTEGER NOT NULL DEFAULT 0);
INSERT INTO incidents VALUES(1,'{"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"orders"}','','2026-11-10T09:00:00Z','owner/repo#1','','','0001-01-01T00:00:00Z','0001-01-01T00:00:00Z',0);
INSERT INTO incidents VALUES(2,NULL,'ic-target/Watchdog','2026-11-10T09:00:00Z','','','','0001-01-01T00:00:00Z','0001-01-01T00:00:00Z',0);
CREATE TABLE members (
	incident_id     INTEGER NOT NULL REFERENCES incidents(id),
	key             TEXT NOT NULL,
	starts_at       TEXT NOT NULL,
	source_kind     TEXT NOT NULL,
	source_instance TEXT NOT NULL,
	name            TEXT NOT NULL,
	severity        TEXT NOT NULL,
	link            TEXT NOT NULL,
	first_seen      TEXT NOT NULL,
	last_seen       TEXT NOT NULL, end_outcome TEXT NOT NULL DEFAULT '', ended_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (incident_id, key, starts_at)
);
INSERT INTO members VALUES(1,'ic-target/aaa','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','warning','http://am/ic-target/aaa','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z','','0001-01-01T00:00:00Z');
INSERT INTO members VALUES(1,'ic-target/bbb','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','critical','http://am/ic-target/bbb','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z','','0001-01-01T00:00:00Z');
INSERT INTO members VALUES(2,'ic-target/ccc','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','info','http://am/ic-target/ccc','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z','','0001-01-01T00:00:00Z');
CREATE TABLE settings (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
INSERT INTO settings VALUES('kill_switch','L1');
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('incidents',2);
COMMIT;
