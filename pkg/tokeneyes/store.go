package tokeneyes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct{ db *sql.DB }

func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &SQLiteStore{db: db}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return s, nil
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS runs(
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  catalog_version TEXT NOT NULL,
  models TEXT NOT NULL,
  total_bytes INTEGER NOT NULL,
  incomplete INTEGER NOT NULL,
  payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_created_at_idx ON runs(created_at DESC);
INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, CURRENT_TIMESTAMP);`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, CURRENT_TIMESTAMP);`)
	return err
}

func (s *SQLiteStore) Save(ctx context.Context, run Run) error {
	run.Sources = privacySources(run.Sources)
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	models, _ := json.Marshal(run.Config.Models)
	var total int64
	for _, source := range run.Sources {
		total += source.Bytes
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,created_at,catalog_version,models,total_bytes,incomplete,payload) VALUES(?,?,?,?,?,?,?)`, run.ID, run.CreatedAt.Format(time.RFC3339Nano), run.CatalogVersion, string(models), total, run.Incomplete, payload)
	return err
}

func (s *SQLiteStore) Get(ctx context.Context, id string) (Run, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM runs WHERE id=?`, id).Scan(&payload)
	if err == sql.ErrNoRows {
		return Run{}, fmt.Errorf("run %q not found", id)
	}
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err := json.Unmarshal(payload, &run); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *SQLiteStore) List(ctx context.Context, limit int) ([]RunSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,created_at,catalog_version,models,total_bytes,incomplete FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunSummary
	for rows.Next() {
		var item RunSummary
		var created, models string
		if err := rows.Scan(&item.ID, &created, &item.CatalogVersion, &models, &item.TotalBytes, &item.Incomplete); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		_ = json.Unmarshal([]byte(models), &item.Models)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) Diff(ctx context.Context, a, b string) (RunDiff, error) {
	runA, err := s.Get(ctx, a)
	if err != nil {
		return RunDiff{}, err
	}
	runB, err := s.Get(ctx, b)
	if err != nil {
		return RunDiff{}, err
	}
	d := RunDiff{RunA: a, RunB: b, Sources: diffSources(runA.Sources, runB.Sources)}
	aResults := make(map[string]ModelResult)
	bResults := make(map[string]ModelResult)
	for _, result := range runA.Results {
		aResults[result.Model] = result
	}
	for _, result := range runB.Results {
		bResults[result.Model] = result
	}
	names := make(map[string]bool)
	for name := range aResults {
		names[name] = true
	}
	for name := range bResults {
		names[name] = true
	}
	for name := range names {
		ra, rb := aResults[name], bResults[name]
		d.Models = append(d.Models, ModelDiff{Model: name, InputTokensA: ra.InputTokens, InputTokensB: rb.InputTokens, InputTokenDelta: rb.InputTokens - ra.InputTokens, ExpectedCostDeltaMicrosUSD: expectedCost(rb) - expectedCost(ra), Components: diffComponents(ra.CountComponents, rb.CountComponents)})
	}
	sort.Slice(d.Models, func(i, j int) bool { return d.Models[i].Model < d.Models[j].Model })
	return d, nil
}

func diffComponents(a, b []CountComponent) []ComponentDiff {
	type key struct{ modality, unit string }
	am, bm := map[key]int64{}, map[key]int64{}
	for _, c := range a {
		am[key{c.Modality, c.Unit}] += c.Expected
	}
	for _, c := range b {
		bm[key{c.Modality, c.Unit}] += c.Expected
	}
	keys := map[key]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	var out []ComponentDiff
	for k := range keys {
		if am[k] != bm[k] {
			out = append(out, ComponentDiff{Modality: k.modality, Unit: k.unit, ValueA: am[k], ValueB: bm[k], Delta: bm[k] - am[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Modality == out[j].Modality {
			return out[i].Unit < out[j].Unit
		}
		return out[i].Modality < out[j].Modality
	})
	return out
}

func diffSources(a, b []Source) SourceDiff {
	am, bm := make(map[string]string), make(map[string]string)
	for _, s := range a {
		am[sourceKey(s)] = s.SHA256
	}
	for _, s := range b {
		bm[sourceKey(s)] = s.SHA256
	}
	var out SourceDiff
	for key, hash := range am {
		other, exists := bm[key]
		if !exists {
			out.Removed = append(out.Removed, key)
		} else if other != hash {
			out.Changed = append(out.Changed, key)
		}
	}
	for key := range bm {
		if _, exists := am[key]; !exists {
			out.Added = append(out.Added, key)
		}
	}
	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	sort.Strings(out.Changed)
	return out
}

func sourceKey(s Source) string {
	if s.Path != "" {
		return s.Path
	}
	return s.Kind + ":" + s.Label
}

func expectedCost(r ModelResult) int64 {
	for _, scenario := range r.Scenarios {
		if scenario.Name == "expected" {
			return scenario.CostMicrosUSD
		}
	}
	if len(r.Scenarios) > 0 {
		return r.Scenarios[len(r.Scenarios)/2].CostMicrosUSD
	}
	return 0
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) HasSourceContent(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM runs`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		if strings.Contains(payload, `"Content"`) || strings.Contains(payload, `"content"`) {
			return true, nil
		}
	}
	return false, rows.Err()
}
