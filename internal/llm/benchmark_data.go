package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/genai-io/san/internal/confdir"
)

// The scores San shows beside a model, as opposed to what the model is.
//
// llmbenchmark.ai publishes one dataset of models and the benchmark scores
// observed for them. San keeps only the four numbers it draws: an intelligence
// and a coding score from Artificial Analysis, each with an Arena Elo fallback
// for the models Artificial Analysis has not measured. The rest of the dataset
// — descriptions, prices, every other benchmark — is dropped on the way in, so
// the cached file is a few hundred rows rather than the 21 MB download.
//
// The file lives beside the models.dev cache and is read through loadBenchmark
// Data, which degrades to an empty set when the file is missing or damaged:
// a score column with no data is a blank, never a failed listing.

// The metrics San reads from the dataset, in preference order per column.
const (
	benchmarkMetricIntelligence    = "aa_intelligence_index"
	benchmarkMetricCoding          = "aa_coding_index"
	benchmarkMetricIntelligenceElo = "arena_text_elo"
	benchmarkMetricCodingElo       = "arena_text_coding_elo"
)

// benchmarkMetrics is the set kept from the dataset.
var benchmarkMetrics = map[string]bool{
	benchmarkMetricIntelligence:    true,
	benchmarkMetricCoding:          true,
	benchmarkMetricIntelligenceElo: true,
	benchmarkMetricCodingElo:       true,
}

// benchmarkModel is one model's reduced row. A zero score means the dataset
// carried no observation for it; no model score is legitimately zero.
type benchmarkModel struct {
	Name            string  `json:"name,omitempty"`
	Intelligence    float64 `json:"intelligence,omitempty"`
	Coding          float64 `json:"coding,omitempty"`
	IntelligenceElo float64 `json:"intelligenceElo,omitempty"`
	CodingElo       float64 `json:"codingElo,omitempty"`
	ObservedAt      string  `json:"observedAt,omitempty"`
}

// benchmarkData is the cached file's shape.
type benchmarkData struct {
	FetchedAt string                    `json:"fetchedAt,omitempty"`
	Models    map[string]benchmarkModel `json:"models"`
}

// benchmarkDataset is the part of llmbenchmark.ai's dataset San reads. The
// decoder skips everything else, so the 21 MB body costs one pass and no
// retained copy.
type benchmarkDataset struct {
	Models []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"models"`
	Scores []struct {
		ModelID    string  `json:"modelId"`
		Metric     string  `json:"metric"`
		Value      float64 `json:"value"`
		ObservedAt string  `json:"observedAt"`
	} `json:"scores"`
}

// benchmarkDataCachePath is where the reduced dataset lives.
func benchmarkDataCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(confdir.Dir(home), "cache", "benchmarks.json")
}

// loadBenchmarkData returns the cached scores, read once per process.
var loadBenchmarkData = sync.OnceValue(func() benchmarkData {
	return readBenchmarkDataAt(benchmarkDataCachePath())
})

// readBenchmarkDataAt reads one cached file. A missing, unreadable or damaged
// file is an empty set: the scores are supplementary, and a model listing must
// not fail over them.
func readBenchmarkDataAt(path string) benchmarkData {
	empty := benchmarkData{Models: map[string]benchmarkModel{}}
	if path == "" {
		return empty
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	var data benchmarkData
	if err := json.Unmarshal(raw, &data); err != nil {
		return empty
	}
	if data.Models == nil {
		data.Models = map[string]benchmarkModel{}
	}
	return data
}

// trimBenchmarkDataset keeps the four metrics San draws, one observation per
// model and metric — the latest the dataset recorded — and drops every model
// with nothing to show.
func trimBenchmarkDataset(raw []byte) (benchmarkData, error) {
	var dataset benchmarkDataset
	if err := json.Unmarshal(raw, &dataset); err != nil {
		return benchmarkData{}, err
	}

	names := make(map[string]string, len(dataset.Models))
	for _, model := range dataset.Models {
		if model.ID != "" {
			names[model.ID] = model.Name
		}
	}

	// values[model][metric] keeps the observation with the latest date. The
	// comparison is lexical, which is what the dataset's YYYY-MM-DD spelling
	// makes correct.
	values := make(map[string]map[string]float64)
	observed := make(map[string]map[string]string)
	for _, score := range dataset.Scores {
		if !benchmarkMetrics[score.Metric] || score.ModelID == "" {
			continue
		}
		byMetric, ok := values[score.ModelID]
		if !ok {
			byMetric = make(map[string]float64)
			values[score.ModelID] = byMetric
			observed[score.ModelID] = make(map[string]string)
		}
		if seen, ok := observed[score.ModelID][score.Metric]; ok && seen > score.ObservedAt {
			continue
		}
		byMetric[score.Metric] = score.Value
		observed[score.ModelID][score.Metric] = score.ObservedAt
	}

	out := benchmarkData{Models: make(map[string]benchmarkModel, len(values))}
	latest := ""
	for id, byMetric := range values {
		if len(byMetric) == 0 {
			continue
		}
		model := benchmarkModel{
			Name:            names[id],
			Intelligence:    byMetric[benchmarkMetricIntelligence],
			Coding:          byMetric[benchmarkMetricCoding],
			IntelligenceElo: byMetric[benchmarkMetricIntelligenceElo],
			CodingElo:       byMetric[benchmarkMetricCodingElo],
		}
		for _, date := range observed[id] {
			if date > model.ObservedAt {
				model.ObservedAt = date
			}
		}
		if model.ObservedAt > latest {
			latest = model.ObservedAt
		}
		out.Models[id] = model
	}
	out.FetchedAt = latest
	return out, nil
}

// writeBenchmarkData replaces the file in one rename, so a reader never sees
// half of it.
func writeBenchmarkData(path string, data benchmarkData) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".benchmarks-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
