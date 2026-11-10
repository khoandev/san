package llm

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"
)

// The fixture carries one row per matching rule, rather than the real dataset:
// the rules are what these tests are about, and the real file changes weekly.
func benchmarkScoresFixture() benchmarkData {
	return benchmarkData{
		FetchedAt: "2026-03-01",
		Models: map[string]benchmarkModel{
			"anthropic/claude-opus-5.5":              {Name: "Claude Opus 5.5", Intelligence: 59.1, Coding: 78},
			"deepseek/deepseek-v4.1-flash":           {Name: "DeepSeek V4.1 Flash", IntelligenceElo: 1463.38, CodingElo: 1503.09},
			"deepseek/deepseek-v4.1-flash-1017":      {Name: "DeepSeek V4.1 Flash 1017", Intelligence: 28.6},
			"nvidia/nemotron-3.5-lightning-20251001": {Name: "Nemotron 3.5 Lightning", Intelligence: 5.5},
			"qwen/qwen3.8-max-preview":               {Name: "Qwen3.8 Max Preview", Coding: 70},
			"qwen/qwen3.8-coder":                     {Name: "Qwen3.8 Coder", Coding: 66},
			"openrouter/qwen3.8-coder":               {Name: "Qwen3.8 Coder", Coding: 1},
			"openai/gpt-5.5":                         {Name: "GPT-5.5", Intelligence: 61},
			"openai/gpt-5.1":                         {Name: "GPT-5.1", Intelligence: 50},
			"z-ai/glm-5.3-flash":                     {Name: "GLM-5.3-Flash", Intelligence: 27.8},
			"reflection/beam-1.0-501b-a23b":          {Name: "Beam", Intelligence: 17.7},
		},
	}
}

func TestBenchmarkIDMatching(t *testing.T) {
	data := benchmarkScoresFixture()
	index := newBenchmarkMatchIndex(data)

	cases := []struct {
		name     string
		provider ProviderID
		modelID  string
		want     string
	}{
		{name: "exact slug", provider: Anthropic, modelID: "claude-opus-5-5", want: "anthropic/claude-opus-5.5"},
		{name: "dotted ID", provider: Anthropic, modelID: "claude-opus-5.5", want: "anthropic/claude-opus-5.5"},
		{name: "free suffix", provider: OpenCodeZen, modelID: "deepseek-v4.1-flash-free", want: "deepseek/deepseek-v4.1-flash"},
		{name: "colon free suffix", provider: OpenCodeZen, modelID: "glm-5.3-flash:free", want: "z-ai/glm-5.3-flash"},
		{name: "dated sibling", provider: OpenCodeZen, modelID: "nemotron-3.5-lightning", want: "nvidia/nemotron-3.5-lightning-20251001"},
		{name: "unique prefix", provider: Alibaba, modelID: "qwen3.8-max", want: "qwen/qwen3.8-max-preview"},
		{name: "provider picks the vendor", provider: Alibaba, modelID: "qwen3.8-coder", want: "qwen/qwen3.8-coder"},
		{name: "ambiguous match", provider: OpenAI, modelID: "gpt-5", want: ""},
		{name: "pinned by benchmark_id", provider: OpenCodeZen, modelID: "beam-free", want: "reflection/beam-1.0-501b-a23b"},
		{name: "no row at all", provider: OpenCodeZen, modelID: "big-pickle", want: ""},
	}
	modelData := loadedModelData()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := benchmarkRowFor(data, index, modelData, c.provider, c.modelID); got != c.want {
				t.Errorf("row for %q/%q = %q, want %q", c.provider, c.modelID, got, c.want)
			}
		})
	}
}

// A model whose ID cannot be matched by spelling still gets its scores when
// San's data names the dataset row.
func TestABenchmarkIDPinsAModelTheSpellingCannotMatch(t *testing.T) {
	data := benchmarkScoresFixture()
	index := newBenchmarkMatchIndex(data)
	modelData := lineups{"acme": {Models: map[string]modelSpec{
		"strange-id": {Name: "Strange", BenchmarkID: "anthropic/claude-opus-5.5"},
		"plain-id":   {Name: "Plain"},
	}}}

	if got := benchmarkRowFor(data, index, modelData, ProviderID("acme"), "strange-id"); got != "anthropic/claude-opus-5.5" {
		t.Errorf("pinned row = %q, want the row the data names", got)
	}
	if got := benchmarkRowFor(data, index, modelData, ProviderID("acme"), "plain-id"); got != "" {
		t.Errorf("unpinned row = %q, want no match", got)
	}
}

func TestBenchmarkScoresPreferTheIndexOverElo(t *testing.T) {
	data := benchmarkScoresFixture()
	index := newBenchmarkMatchIndex(data)

	// Artificial Analysis measured this one: both halves are its index, and
	// neither is marked as a rank.
	measured := modelScoresFor(data, index, loadedModelData(), Anthropic, "claude-opus-5-5")
	if measured == nil || measured.Intelligence == nil || measured.Coding == nil {
		t.Fatalf("scores = %+v, want both halves", measured)
	}
	if measured.Intelligence.Value != 59.1 || measured.Intelligence.Metric != benchmarkMetricIntelligence {
		t.Errorf("intelligence = %+v, want the AA index", measured.Intelligence)
	}
	if measured.Intelligence.Elo() {
		t.Error("an index was marked as an Elo")
	}

	// Artificial Analysis has not measured this one: the Arena rank answers,
	// and says so, so the picker can mark it.
	ranked := modelScoresFor(data, index, loadedModelData(), OpenCodeZen, "deepseek-v4.1-flash-free")
	if ranked == nil || ranked.Intelligence == nil || ranked.Coding == nil {
		t.Fatalf("scores = %+v, want both Elo halves", ranked)
	}
	if !ranked.Intelligence.Elo() || ranked.Intelligence.Metric != benchmarkMetricIntelligenceElo {
		t.Errorf("intelligence = %+v, want the Arena rank", ranked.Intelligence)
	}
	if ranked.Coding.Value != 1503.09 || !ranked.Coding.Elo() {
		t.Errorf("coding = %+v, want the Arena coding rank", ranked.Coding)
	}

	// A model with no row carries no scores rather than zeroes.
	if got := modelScoresFor(data, index, loadedModelData(), OpenCodeZen, "big-pickle"); got != nil {
		t.Errorf("scores = %+v, want nil", got)
	}
}

// withBenchmarkDataset installs a dataset for one test, in place of the cached
// file, and restores the real loaders afterwards.
func withBenchmarkDataset(t *testing.T, data benchmarkData) {
	t.Helper()
	previousData, previousIndex := loadBenchmarkData, benchmarkIndex
	loadBenchmarkData = func() benchmarkData { return data }
	benchmarkIndex = func() *benchmarkMatchIndex { return newBenchmarkMatchIndex(data) }
	t.Cleanup(func() {
		loadBenchmarkData, benchmarkIndex = previousData, previousIndex
	})
}

// What the listing carries and what the helper returns are the same answer:
// the app fills a cached ModelInfo through the helper, and must not disagree
// with a freshly listed one.
func TestListingCarriesTheScoresTheHelperReturns(t *testing.T) {
	withBenchmarkDataset(t, benchmarkScoresFixture())

	server := listing(t, `{"object":"list","data":[{"id":"claude-opus-5-5","object":"model"},{"id":"big-pickle","object":"model"}]}`)
	vendor, ok := catalog.Find("anthropic")
	if !ok {
		t.Fatal("the catalog has no anthropic vendor")
	}
	p := newVendorProvider("anthropic:api_key", vendor, endpoint.Config{APIKey: "k", BaseURL: server.URL})

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}

	listed, present := byID["claude-opus-5-5"]
	if !present {
		t.Fatalf("claude-opus-5-5 is missing from %+v", models)
	}
	if listed.Scores == nil {
		t.Fatal("the listed model carries no scores")
	}
	want := ModelScoresFor(Anthropic, "claude-opus-5-5")
	if !reflect.DeepEqual(listed.Scores, want) {
		t.Errorf("listing scores = %+v, helper scores = %+v", listed.Scores, want)
	}

	if unknown, present := byID["big-pickle"]; present && unknown.Scores != nil {
		t.Errorf("a model with no dataset row carries scores: %+v", unknown.Scores)
	}
}

// Scores are additive in the cached listing: a ModelInfo written by an older
// San still decodes, and one written now carries its scores across.
func TestScoresSurviveTheModelCache(t *testing.T) {
	var old ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"a","name":"A"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Scores != nil {
		t.Errorf("a listing without scores decoded as %+v", old.Scores)
	}

	with := ModelInfo{ID: "a", Name: "A", Scores: &ModelScores{
		Intelligence: &ModelScore{Value: 59.1, Metric: benchmarkMetricIntelligence},
		Coding:       &ModelScore{Value: 1503.09, Metric: benchmarkMetricCodingElo},
	}}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	var back ModelInfo
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Scores, with.Scores) {
		t.Errorf("scores round-tripped as %+v, want %+v", back.Scores, with.Scores)
	}

	raw, err = json.Marshal(ModelInfo{ID: "b", Name: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "scores") {
		t.Errorf("a model with no scores wrote the field: %s", raw)
	}
}
