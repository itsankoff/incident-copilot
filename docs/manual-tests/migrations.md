# Manual test: Store migrations

This test checks the Control plane store's goose migrations (issue #94) on your machine:
- running `make migrate` against a local file;
- creating a SQL migration and a Go data migration;
- adopting a database that the store's old in-code DDL built;
- a failing migration rolling back whole;
- the CI check that stops edits to merged migrations.

Run it whenever `internal/store`, its migrations, `cmd/migrate`, or the goose version changes.

Everything runs locally against files under `build/dev/`. Nothing touches a cluster. Step 7 covers the one part that needs the demo: the first Control plane start on its existing volume.

## Before you start

- **Go**, at the version in `go.mod`.
- **The `sqlite3` CLI**, to load the fixtures and look inside the files. It ships with macOS. On Linux, install `sqlite3`.
- **A clean tree.** Steps 2 and 3 create migration files and step 8 commits on a scratch branch, and each step removes what it created.

Run every command from the repository root, in one shell (bash or zsh).

## 1. Status and up on a new file

```sh
rm -f build/dev/state.db
make migrate                  # status of build/dev/state.db, created empty
```

Expect every migration `pending`, starting with `1  pending  00001_adopt_legacy.go`. The Go migration is listed by name.

```sh
make migrate CMD=up
make migrate
```

Expect `up` to log `migration completed` for each migration, then `successfully migrated database`, and every migration to be `applied`. The tables exist:

```sh
sqlite3 build/dev/state.db .tables
# expect: goose_db_version  incidents  members  settings
```

## 2. Create a SQL migration, apply it, redo it, undo it

```sh
make migration NAME=manual_probe
```

Expect `Created new file: internal/store/migrations/0000N_manual_probe.sql`, where N is one more than the highest existing number, the Go files included. Write a change into it:

```sh
f=$(ls internal/store/migrations/*_manual_probe.sql)
cat > "$f" <<'EOF'
-- +goose Up
ALTER TABLE incidents ADD COLUMN probe TEXT NOT NULL DEFAULT '';
-- +goose Down
ALTER TABLE incidents DROP COLUMN probe;
EOF
make migrate CMD=up
make migrate CMD=redo          # down one, then up one
sqlite3 build/dev/state.db "SELECT count(*) FROM pragma_table_info('incidents') WHERE name = 'probe'"
# expect 1
make migrate CMD=down
make migrate
```

Expect the redo to log the `ALTER TABLE incidents DROP COLUMN probe` and then the `ADD COLUMN`. After `down`, the status shows `manual_probe` as `pending` and every other migration as `applied`.

The golden schema now catches the change:

```sh
go test ./internal/store
# expect FAIL: schema differs from testdata/schema.txt, showing the probe column
rm "$f"
go test ./internal/store
# expect ok
```

## 3. Create a Go data migration

```sh
make migration NAME=manual_backfill KIND=go
g=$(ls internal/store/migrations/*_manual_backfill.go)
cat "$g"
```

Expect a `package migrations` file whose `init` calls `register(upManualBackfill, downManualBackfill)`. It compiles as written, and `cmd/migrate` sees it:

```sh
make migrate CMD=up
make migrate
# expect the last line: N  applied  0000N_manual_backfill.go
make migrate CMD=down
rm "$g"
```

## 4. Adopt databases built before migrations

The fixtures in `internal/store/testdata/` are `sqlite3 .dump`s of databases that the real store code wrote:
- `pre85.sql`: before PR #85;
- `post85.sql`: after PR #85;
- `post88.sql`: after PR #88, with the kill switch set.

Each holds two Incidents and three members.

```sh
for s in pre85 post85 post88; do
  rm -f build/dev/$s.db
  sqlite3 build/dev/$s.db < internal/store/testdata/$s.sql
  make migrate DB=build/dev/$s.db CMD=up >/dev/null 2>&1
  echo "$s: $(sqlite3 build/dev/$s.db 'SELECT count(*) FROM incidents') incidents," \
    "$(sqlite3 build/dev/$s.db 'SELECT count(*) FROM members') members," \
    "version $(sqlite3 build/dev/$s.db 'SELECT max(version_id) FROM goose_db_version')"
done
```

Expect `2 incidents, 3 members` and the latest version for each. The three end with the same columns as a fresh database:

```sh
cols() { sqlite3 "$1" "SELECT m.name, p.name, p.type, p.\"notnull\", p.dflt_value, p.pk FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name NOT LIKE 'goose_%' AND m.name NOT LIKE 'sqlite_%' ORDER BY m.name, p.cid"; }
for s in pre85 post85 post88; do diff <(cols build/dev/state.db) <(cols build/dev/$s.db) && echo "$s: same schema"; done
```

`pre85` gains the lifecycle columns, and `state` reads `open` on its old rows. `post85` gains the `settings` table. `post88` is only stamped with versions.

Running `make migrate DB=build/dev/post88.db CMD=up` a second time applies nothing: expect no `migration completed` line.

## 5. The Control plane adopts a database when it starts

This is what happens on the demo's volume on the next deploy, here on a local copy:

```sh
rm -f build/dev/cp.db && sqlite3 build/dev/cp.db < internal/store/testdata/pre85.sql
go build -o build/dev/cp ./cmd/incident-copilot
IC_STATE_PATH=build/dev/cp.db IC_AUDIT_PATH=build/dev/cp-audit.jsonl \
IC_LISTEN_ADDR=127.0.0.1:28480 IC_DEBUG_ADDR=127.0.0.1:28481 \
IC_ALERTMANAGER_INSTANCE=ic-target IC_ALERTMANAGER_TOKEN=t IC_ALERTMANAGER_LOCATION=ic-target \
  build/dev/cp -role=control-plane | grep -E 'store migration applied|control plane listening'
# stop it with Ctrl-C once it logs "control plane listening"
```

Expect one `store migration applied` line for each migration, with `migration` and `source`, then `control plane listening`. Run it again: expect no `store migration applied` line. Pick other ports if 28480 or 28481 are taken.

## 6. A failing migration rolls back whole

```sh
cat > internal/store/migrations/99999_manual_broken.sql <<'EOF'
-- +goose Up
CREATE TABLE later (x TEXT);
INSERT INTO nope VALUES (1);
-- +goose Down
DROP TABLE later;
EOF
make migrate DB=build/dev/cp.db CMD=up; echo "exit=$?"
```

Expect `partial migration error (type:sql,version:99999): SQL logic error: no such table: nope` and a non-zero exit. Nothing changed:

```sh
sqlite3 build/dev/cp.db "SELECT max(version_id) FROM goose_db_version; SELECT count(*) FROM sqlite_master WHERE name = 'later'"
# expect the previous version, then 0
rm internal/store/migrations/99999_manual_broken.sql
```

The Control plane fails the same way: `store.Open` returns the error, and the process exits 1 with `fatal: store …: partial migration error …`.

## 7. The demo volume upgrades with no manual step (live, on the demo)

On the long-lived demo with an existing `control-plane-state` volume, deploy this change as usual:

```sh
make control-plane VERSION="$(git rev-parse --short HEAD)-$(date +%s)"
kubectl --context kind-ic-control-plane -n incident-copilot logs deploy/control-plane -c control-plane \
  | jq -c 'select(.msg == "store migration applied" or .msg == "control plane listening") | {msg, migration, source, version}'
```

Expect a `store migration applied` line for each migration on the first start after the upgrade, then `control plane listening`. Incidents opened before the upgrade keep their tickets: a firing alert joins its existing Incident rather than opening a new ticket. A later restart logs no `store migration applied` line.

## 8. Merged migrations cannot change

On a scratch branch, edit a merged migration, commit, and run the CI check:

```sh
git switch -c scratch/migration-edit
echo "-- edited" >> internal/store/migrations/00002_baseline.sql
git commit -qam "edit a merged migration"
make check-migrations
# expect: merged migrations never change; add a new migration instead:
#         M	internal/store/migrations/00002_baseline.sql
git switch - && git branch -D scratch/migration-edit
make check-migrations
# expect: merged migrations unchanged since origin/master
```

Renaming (`R`) or deleting (`D`) a merged migration fails the same way. Adding a new one passes.

## Clean up

```sh
rm -f build/dev/*.db build/dev/cp build/dev/cp-audit.jsonl
git status --short internal/store/migrations   # expect nothing
```
