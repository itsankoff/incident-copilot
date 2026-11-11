# Contributing

## Where things are

- [GitHub Issues](https://github.com/itsankoff/incident-copilot/issues): the task plan and current status, starting from the [Wayfinder map](https://github.com/itsankoff/incident-copilot/issues/3).
- [`docs/design/`](docs/design): design docs, one per milestone that needs one.
- [`docs/adr/`](docs/adr): architecture decision records and the [architecture diagram](docs/adr/arch-diagram.md).
- [ADR 0003: Safety invariants](docs/adr/0003-trust-model-and-safety-invariants.md#safety-invariants): never weaken these. Relaxing one needs a new ADR that supersedes ADR 0003.

Check what actually exists before assuming a package, command, or file is present. Much of the architecture is still the target, not code.

## Before a PR

```sh
make build test vet lint
```

## Engineering conventions

- **Interfaces at every external boundary:** the LLM, ticketing, approval, the k8s client, Prometheus, Loki, and GitHub. This lets unit tests use fakes with no network access. Tests that need a real cluster or real APIs belong behind a build tag or in the eval harness.
- Pass `context.Context` through every call that touches I/O, and put timeouts on all outbound calls.
- Use structured logging (`log/slog`). Operational logs and the audit log are separate streams.
- Favor the standard library and a few well-known dependencies (client-go, the MCP Go SDK, provider SDKs). Justify any new dependency.
- Keep prompts in versioned files, not scattered string literals, so the eval harness can attribute score changes to them.

## Migrations

The Control plane keeps two SQLite databases, and each one's schema changes only through versioned [goose](https://github.com/pressly/goose) migrations in its own directory:

| Database | Migrations | Applied by |
|---|---|---|
| The state (`IC_STATE_PATH`) | [`internal/store/migrations/`](internal/store/migrations) | `store.Open` |
| The demo's local tickets (`tickets.db`, next to the state) | [`internal/localtickets/migrations/`](internal/localtickets/migrations) | `localtickets.Open` |

The migrations are embedded in the binary, and [`internal/migrate`](internal/migrate) applies the pending ones when the Control plane opens each database. No non-test code outside the migrations directories runs `CREATE`, `ALTER`, or `DROP`. There is no ORM.

goose is the one dependency added for this (#94). It is MIT-licensed and pure Go over the existing `modernc.org/sqlite` driver, so builds stay `CGO_ENABLED=0`. It runs plain SQL files and Go data migrations, and it records each version in the migration's own transaction. It pulls in four small modules: `mfridman/interpolate`, `sethvargo/go-retry`, `go.uber.org/multierr`, and `golang.org/x/sync`. golang-migrate, dbmate, and Atlas were ruled out in the issue's research.

### Adding a migration

The commands below default to the state database. Add `SCHEMA=tickets` for the tickets database.

1. Run `make migration NAME=add_priority`. It writes the next numbered file, such as `00004_add_priority.sql`. Numbers are sequential, five digits, and count the Go files too.
2. Write the change under `-- +goose Up` and its undo under `-- +goose Down`. Every SQL migration has a Down. Downs never run in production: they exist for `make migrate CMD=redo` and the tests. Write a table rebuild's Down by hand, because generated ones are wrong for SQLite.
3. Rewrite the golden schema with `go test ./internal/store -update` (or `./internal/localtickets`), which updates `testdata/schema.txt` in that package. Commit it with the migration, so the PR shows the schema change column by column.
4. Update the queries.
5. Try it on a local file with `make migrate CMD=up`, then `make migrate CMD=redo`, and look at the result with `make migrate` (status). `DB` picks the file, and defaults to `build/dev/<schema>.db`.

**Schema changes (DDL) go only in `.sql` files.** Tools that read a migrations directory, such as sqlc, Atlas, or a future ORM's drift check, see only the SQL. `TestSQLFilesAloneBuildTheSchema` enforces this for the state.

**A data migration that SQL cannot express** goes in Go: run `make migration NAME=backfill_priority KIND=go`. The generated `NNNNN_name.go` registers itself with `register(up, down)`, which runs in goose's transaction. A Go migration only changes data, and reads rows into its own local types, never the app's, so it keeps working as they change. The state's `00001_adopt_legacy.go` is the one exception: it completes databases that the store built before migrations existed. The tickets database has no Go migrations yet; its first one needs a `register` like the state's.

**Each migration commits whole.** A migration and its version record commit in one transaction. A migration that fails, or leaves a foreign key violation, leaves the schema and the version as they were, and the database's `Open` fails with its error.

**Rebuilding a table** (SQLite's [12-step rebuild](https://sqlite.org/lang_altertable.html#otheralter), needed for most changes other than adding a column) is a plain migration file. Migrations run with foreign keys off, so a parent table, such as `incidents`, which `members` references, can be dropped and recreated inside the transaction. Before each migration commits, `PRAGMA foreign_key_check` must be empty.

### Rules

- **Never edit, rename, or delete a migration once it has merged.** Add a new one. CI runs `make check-migrations`. It fails when a numbered file under an `internal/**/migrations/` directory on `master` changes.
- **Keep the previous release working on the new schema (expand/contract).** Add a column with a default, and backfill it in a later migration if needed. Drop or rename a column, table, or constraint only in a release after the code stopped reading it. Rolling back the image is then safe: an older binary on a newer database starts, and logs the warning `database schema is newer than this binary`.
- **Bump goose with care.** Use `make check-libc` to check that `modernc.org/libc` is still the version `modernc.org/sqlite` pins (CI runs it too). goose v3.28 and later need Go 1.26.

### On deploy

There is no separate step. The Control plane runs as one replica with `strategy: Recreate` on a `ReadWriteOnce` volume, so only one process ever migrates its databases. It logs `migration applied`, with the database's path, once for each migration it applies. A failed migration rolls back: the pod exits with the migration's error, and the previous image still runs on the database. Databases that the old in-code DDL built are adopted on their first start, keeping their rows:
- the state from before or after PR #85, and from after PR #88;
- the tickets from after PR #91.

## Configuration

Configuration comes from environment variables, with `.env` supported for local use (`.env` is gitignored, so never commit it). The variables are listed in the [README](README.md#configuration). When you add a variable, update that table in the same change.

## Evaluation harness

Treat `make eval` as a regression suite. Run it before and after any change to prompts, models, or tools, and report the difference.

When you add a fault, add its ground truth to the harness in the same change.
