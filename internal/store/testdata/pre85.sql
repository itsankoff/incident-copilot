-- sqlite3 .dump of a database written by store.Open before PR #85 (8c39951): two Incidents, three members.
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE incidents (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	target     TEXT,              -- JSON signal.Target, NULL when there is none
	key        TEXT NOT NULL,     -- the opening Signal's key when there is no Target
	opened_at  TEXT NOT NULL,
	ticket_ref TEXT NOT NULL DEFAULT ''
);
INSERT INTO incidents VALUES(1,'{"platform":"kubernetes","location":"ic-target","namespace":"demo","kind":"Deployment","name":"orders"}','','2026-11-10T09:00:00Z','owner/repo#1');
INSERT INTO incidents VALUES(2,NULL,'ic-target/Watchdog','2026-11-10T09:00:00Z','');
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
	last_seen       TEXT NOT NULL,
	PRIMARY KEY (incident_id, key, starts_at)
);
INSERT INTO members VALUES(1,'ic-target/aaa','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','warning','http://am/ic-target/aaa','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z');
INSERT INTO members VALUES(1,'ic-target/bbb','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','critical','http://am/ic-target/bbb','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z');
INSERT INTO members VALUES(2,'ic-target/ccc','2026-11-10T09:00:00Z','alertmanager','ic-target','KubePodCrashLooping','info','http://am/ic-target/ccc','2026-11-10T09:00:00Z','2026-11-10T09:01:00Z');
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('incidents',2);
COMMIT;
