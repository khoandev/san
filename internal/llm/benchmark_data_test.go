package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The dataset sample: three models, four wanted metrics, one unknown metric,
// and two observations of the same metric for the same model.
const benchmarkSample = `{
  "models": [
    {"id": "a/one", "name": "One"},
    {"id": "b/two", "name": "Two"},
    {"id": "c/three", "name": "Three"}
  ],
  "scores": [
    {"modelId": "a/one", "metric": "aa_intelligence_index", "value": 10, "observedAt": "2026-01-01"},
    {"modelId": "a/one", "metric": "aa_intelligence_index", "value": 15, "observedAt": "2026-02-01"},
    {"modelId": "a/one", "metric": "aa_intelligence_index", "value": 20, "observedAt": "2026-03-01"},
    {"modelId": "a/one", "metric": "aa_coding_index", "value": 30, "observedAt": "2026-03-01"},
    {"modelId": "a/one", "metric": "aa_hle", "value": 99, "observedAt": "2026-03-01"},
    {"modelId": "b/two", "metric": "arena_text_elo", "value": 1400, "observedAt": "2026-01-05"},
    {"modelId": "b/two", "metric": "arena_text_coding_elo", "value": 1350, "observedAt": "2026-01-05"},
    {"modelId": "c/three", "metric": "aa_hle", "value": 1, "observedAt": "2026-01-05"}
  ]
}`

func TestTrimBenchmarkDatasetKeepsTheWantedMetricsOnly(t *testing.T) {
	data, err := trimBenchmarkDataset([]byte(benchmarkSample))
	if err != nil {
		t.Fatal(err)
	}

	if len(data.Models) != 2 {
		t.Fatalf("kept %d models, want 2 (one carries no wanted metric)", len(data.Models))
	}
	one, ok := data.Models["a/one"]
	if !ok {
		t.Fatal("a/one was dropped")
	}
	if one.Name != "One" {
		t.Errorf("name = %q, want %q", one.Name, "One")
	}
	if one.Intelligence != 20 {
		t.Errorf("intelligence = %v, want the latest observation 20", one.Intelligence)
	}
	if one.Coding != 30 {
		t.Errorf("coding = %v, want 30", one.Coding)
	}
	if one.ObservedAt != "2026-03-01" {
		t.Errorf("observedAt = %q, want the latest date", one.ObservedAt)
	}
	if one.IntelligenceElo != 0 || one.CodingElo != 0 {
		t.Errorf("Elo values were invented: %+v", one)
	}

	two, ok := data.Models["b/two"]
	if !ok {
		t.Fatal("b/two was dropped")
	}
	if two.IntelligenceElo != 1400 || two.CodingElo != 1350 {
		t.Errorf("Elo fallbacks = %+v, want 1400/1350", two)
	}
	if _, ok := data.Models["c/three"]; ok {
		t.Error("c/three carries only an unwatched metric and should be dropped")
	}
	if data.FetchedAt != "2026-03-01" {
		t.Errorf("fetchedAt = %q, want the latest observation date", data.FetchedAt)
	}
}

func TestBenchmarkCacheRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	want := benchmarkData{
		FetchedAt: "2026-03-01",
		Models: map[string]benchmarkModel{
			"a/one": {Name: "One", Intelligence: 20, Coding: 30, ObservedAt: "2026-03-01"},
		},
	}
	if err := writeBenchmarkData(path, want); err != nil {
		t.Fatal(err)
	}
	got := readBenchmarkDataAt(path)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
}

func TestADamagedBenchmarkCacheReadsAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readBenchmarkDataAt(path)
	if len(got.Models) != 0 {
		t.Errorf("a damaged cache produced %d models", len(got.Models))
	}

	if got := readBenchmarkDataAt(filepath.Join(t.TempDir(), "missing.json")); len(got.Models) != 0 {
		t.Errorf("a missing cache produced %d models", len(got.Models))
	}
}

func TestAFreshBenchmarkCacheIsNotRefetched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	if err := writeBenchmarkData(path, benchmarkData{Models: map[string]benchmarkModel{}}); err != nil {
		t.Fatal(err)
	}
	// An unreachable URL proves the freshness check short-circuits the fetch.
	if err := refreshBenchmarkData(context.Background(), "http://127.0.0.1:1/dataset.json", path, time.Now()); err != nil {
		t.Fatalf("a fresh cache was refetched: %v", err)
	}
}

func TestAStaleBenchmarkCacheIsRefetched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	if err := writeBenchmarkData(path, benchmarkData{Models: map[string]benchmarkModel{"old/model": {}}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-benchmarkDataMaxAge - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(benchmarkSample))
	}))
	defer server.Close()

	if err := refreshBenchmarkData(context.Background(), server.URL, path, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := readBenchmarkDataAt(path)
	if _, ok := got.Models["a/one"]; !ok {
		t.Errorf("the refreshed cache has no new model: %+v", got.Models)
	}
	if _, ok := got.Models["old/model"]; ok {
		t.Error("the refreshed cache still carries the old model")
	}
}

func TestBenchmarkRefreshFailureKeepsTheCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	if err := writeBenchmarkData(path, benchmarkData{Models: map[string]benchmarkModel{"old/model": {Name: "Old"}}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-benchmarkDataMaxAge - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	if err := refreshBenchmarkData(context.Background(), server.URL, path, time.Now()); err == nil {
		t.Fatal("a failed fetch reported success")
	}
	got := readBenchmarkDataAt(path)
	if model, ok := got.Models["old/model"]; !ok || model.Name != "Old" {
		t.Errorf("the cache was lost on a failed refresh: %+v", got.Models)
	}
}

// The benchmark cache is its own file: a model-scores.json from an older
// experiment must be neither read nor written.
func TestBenchmarkCacheDoesNotTouchModelScores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := benchmarkDataCachePath()
	if filepath.Base(path) != "benchmarks.json" {
		t.Fatalf("benchmark cache path = %q, want benchmarks.json", path)
	}
	legacy := filepath.Join(filepath.Dir(path), "model-scores.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"legacy":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// With only the legacy file present, the loader reads nothing.
	if got := readBenchmarkDataAt(path); len(got.Models) != 0 {
		t.Errorf("the loader read %d models from a file it should not know", len(got.Models))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(benchmarkSample))
	}))
	defer server.Close()
	if err := refreshBenchmarkData(context.Background(), server.URL, path, time.Now()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"legacy":true}` {
		t.Errorf("model-scores.json was rewritten: %q", raw)
	}
	if got := readBenchmarkDataAt(path); len(got.Models) != 2 {
		t.Errorf("benchmarks.json held %d models, want 2", len(got.Models))
	}
}

// A payload that parses but carries none of the metrics San draws is a failed
// refresh, not an empty dataset: writing it would blank the scores for a week.
func TestADatasetWithNoWantedMetricsIsNotWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "benchmarks.json")
	if err := writeBenchmarkData(path, benchmarkData{Models: map[string]benchmarkModel{"old/model": {Name: "Old"}}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-benchmarkDataMaxAge - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"id":"a/one","name":"One"}],"scores":[{"modelId":"a/one","metric":"aa_hle","value":1}]}`))
	}))
	defer server.Close()

	if err := refreshBenchmarkData(context.Background(), server.URL, path, time.Now()); err == nil {
		t.Fatal("an unusable dataset reported success")
	}
	if got := readBenchmarkDataAt(path); len(got.Models) != 1 {
		t.Errorf("the cache was replaced with %d models", len(got.Models))
	}
}
