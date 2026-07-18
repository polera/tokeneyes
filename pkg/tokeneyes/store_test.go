package tokeneyes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLitePrivacyHistoryAndDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	base := Run{SchemaVersion: SchemaVersion, ID: "a", CreatedAt: time.Now().UTC(), CatalogVersion: CatalogVersion, Config: RunConfig{Models: []string{"gpt"}}, Sources: []Source{{Label: "prompt", Kind: "prompt", SHA256: "old", Bytes: 16, Content: []byte("private sentinel"), ExtractedText: []byte("extracted sentinel")}}, Results: []ModelResult{{Model: "gpt", InputTokens: 10, CountComponents: []CountComponent{{Modality: "text", Unit: "tokens", Expected: 10}}, Scenarios: []OutputScenario{{Name: "expected", CostMicrosUSD: 100}}}}}
	if err := store.Save(ctx, base); err != nil {
		t.Fatal(err)
	}
	next := base
	next.ID = "b"
	next.Sources = []Source{{Label: "prompt", Kind: "prompt", SHA256: "new", Bytes: 7, Content: []byte("changed")}}
	next.Results = []ModelResult{{Model: "gpt", InputTokens: 14, CountComponents: []CountComponent{{Modality: "text", Unit: "tokens", Expected: 12}, {Modality: "image", Unit: "tokens", Expected: 2}}, Scenarios: []OutputScenario{{Name: "expected", CostMicrosUSD: 130}}}}
	if err := store.Save(ctx, next); err != nil {
		t.Fatal(err)
	}
	runs, err := store.List(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("history=%+v err=%v", runs, err)
	}
	d, err := store.Diff(ctx, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Sources.Changed) != 1 || d.Models[0].InputTokenDelta != 4 || d.Models[0].ExpectedCostDeltaMicrosUSD != 30 {
		t.Fatalf("diff=%+v", d)
	}
	if len(d.Models[0].Components) != 2 {
		t.Fatalf("component diff=%+v", d.Models[0].Components)
	}
	var migrations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil || migrations != 2 {
		t.Fatalf("migrations=%d err=%v", migrations, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private sentinel") || strings.Contains(string(b), "extracted sentinel") || strings.Contains(string(b), "changed") {
		t.Fatal("SQLite retained source contents")
	}
}

func TestMigrationLeavesV1PayloadReadable(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := `{"schema_version":"tokeneyes.run.v1","id":"old","created_at":"2026-01-01T00:00:00Z","catalog_version":"old","config":{"models":["gpt"]},"sources":[{"label":"x","kind":"file","sha256":"h","bytes":1}],"results":[{"model":"gpt","input_tokens":1}],"incomplete":false}`
	if _, err = store.db.Exec(`INSERT INTO runs(id,created_at,catalog_version,models,total_bytes,incomplete,payload) VALUES(?,?,?,?,?,?,?)`, "old", "2026-01-01T00:00:00Z", "old", `["gpt"]`, 1, false, payload); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(context.Background(), "old")
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != "tokeneyes.run.v1" || run.Results[0].InputTokens != 1 {
		t.Fatalf("old run=%+v", run)
	}
}
