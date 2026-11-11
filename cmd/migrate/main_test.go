package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// create numbers the new file after the highest migration in the directory,
// Go files included, and writes a Down section to fill in.
func TestCreateNumbersAfterTheLastMigration(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"migrations.go", "00001_adopt_legacy.go", "00002_baseline.sql"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("package migrations\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := run(ctx, []string{"-dir", dir, "create", "add_priority"}, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"-dir", dir, "create", "backfill_priority", "go"}, new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	sqlFile, err := os.ReadFile(filepath.Join(dir, "00003_add_priority.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sqlFile), "-- +goose Up") || !strings.Contains(string(sqlFile), "-- +goose Down") {
		t.Errorf("00003_add_priority.sql:\n%s", sqlFile)
	}
	goFile, err := os.ReadFile(filepath.Join(dir, "00004_backfill_priority.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goFile), "register(upBackfillPriority, downBackfillPriority)") {
		t.Errorf("00004_backfill_priority.go:\n%s", goFile)
	}
}

// status, up, down and redo work on a local file and see the Go migrations.
func TestCommandsOnALocalFile(t *testing.T) {
	db := filepath.Join(t.TempDir(), "dev", "state.db")
	ctx := context.Background()
	status := func() string {
		t.Helper()
		var out strings.Builder
		if err := run(ctx, []string{"-db", db, "status"}, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if s := status(); strings.Count(s, "pending") != strings.Count(s, "\n") || !strings.Contains(s, "00001_adopt_legacy.go") {
		t.Fatalf("status of a new file:\n%s", s)
	}
	for _, cmd := range []string{"up", "down", "redo"} {
		if err := run(ctx, []string{"-db", db, cmd}, new(strings.Builder)); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	// Everything is applied but the last migration, which down undid.
	lines := strings.Split(strings.TrimSpace(status()), "\n")
	for i, l := range lines {
		if want := map[bool]string{true: "pending", false: "applied"}[i == len(lines)-1]; !strings.Contains(l, want) {
			t.Fatalf("status after up, down, redo, line %d is not %s:\n%s", i+1, want, strings.Join(lines, "\n"))
		}
	}
	if err := run(ctx, []string{"-db", db, "sideways"}, new(strings.Builder)); err == nil {
		t.Fatal("an unknown command succeeded")
	}
}
