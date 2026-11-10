package llm

import (
	"slices"
	"strings"
	"sync"
)

// What a model scores, as the picker draws it.
//
// llmbenchmark.ai publishes one number per benchmark per model; San keeps four
// of them and shows two. The pair is resolved from the cached dataset by model
// ID, which the two sides spell differently — "anthropic/claude-opus-5.5" there,
// "claude-opus-5-5" here — so the lookup normalises both, tries a match with the
// version suffix dropped, then a unique prefix, and prefers the row filed under
// the provider's own vendor. A model that still cannot be matched may have its
// row pinned by benchmark_id in San's own data; beam-free is one.
//
// A model with no scores is the normal case, not a failure: the dataset covers
// a fraction of what San lists, and a blank column is the honest answer.

// ModelScores is the pair of scores San draws beside a model. Either half can
// be absent; both absent is no scores at all.
type ModelScores struct {
	Intelligence *ModelScore `json:"intelligence,omitempty"`
	Coding       *ModelScore `json:"coding,omitempty"`
}

// ModelScore is one score, as the dataset published it. Value stays on the
// dataset's own scale — an index out of 100, or an Arena Elo — and Metric says
// which, so the picker can mark the second rather than pretend the two compare.
type ModelScore struct {
	Value  float64 `json:"value"`
	Metric string  `json:"metric,omitempty"`
}

// Elo reports whether the score is an Arena Elo rather than an index on the
// dataset's points scale.
func (s ModelScore) Elo() bool {
	return strings.HasPrefix(s.Metric, "arena_")
}

// benchmarkVendors maps San's provider IDs onto the vendor the dataset files a
// model under, where the two spellings differ. A provider not listed here is
// looked up under its own name.
var benchmarkVendors = map[ProviderID]string{
	BigModel: "z-ai",
	Mimo:     "xiaomi",
	Moonshot: "moonshotai",
	Alibaba:  "qwen",
	MinMax:   "minimax",
	// The dataset has no vendor by this name, and Zen resells everyone's
	// models: no preference is better than a wrong one.
	OpenCodeZen: "",
}

// benchmarkVendorFor is the vendor prefix a provider's models carry in the
// dataset.
func benchmarkVendorFor(provider ProviderID) string {
	if vendor, ok := benchmarkVendors[provider]; ok {
		return vendor
	}
	return string(provider)
}

// ModelScoresFor returns the scores to draw for one of a provider's models, or
// nil when the cached dataset does not know it. It is the answer the listing
// path uses, for a caller holding a ModelInfo written before scores existed.
func ModelScoresFor(provider ProviderID, modelID string) *ModelScores {
	return modelScoresFor(loadBenchmarkData(), benchmarkIndex(), loadedModelData(), provider, modelID)
}

// modelScoresFor is ModelScores with the dataset, its index and San's data
// supplied, so a test can answer without the network or a cache file.
func modelScoresFor(data benchmarkData, index *benchmarkMatchIndex, modelData lineups, provider ProviderID, modelID string) *ModelScores {
	if len(data.Models) == 0 || modelID == "" {
		return nil
	}
	row, ok := data.Models[benchmarkRowFor(data, index, modelData, provider, modelID)]
	if !ok {
		return nil
	}
	return row.scores()
}

// benchmarkRowFor is the dataset row San draws a model's scores from: the one
// San's data pins, else the one the ID resolves to.
func benchmarkRowFor(data benchmarkData, index *benchmarkMatchIndex, modelData lineups, provider ProviderID, modelID string) string {
	if pinned := pinnedBenchmarkID(modelData, provider, modelID); pinned != "" {
		if _, ok := data.Models[pinned]; ok {
			return pinned
		}
	}
	return index.resolve(provider, modelID)
}

// scores turns one reduced dataset row into what the picker draws, preferring
// Artificial Analysis' index over the Arena rank it falls back to.
func (m benchmarkModel) scores() *ModelScores {
	scores := ModelScores{
		Intelligence: pickScore(m.Intelligence, benchmarkMetricIntelligence, m.IntelligenceElo, benchmarkMetricIntelligenceElo),
		Coding:       pickScore(m.Coding, benchmarkMetricCoding, m.CodingElo, benchmarkMetricCodingElo),
	}
	if scores.Intelligence == nil && scores.Coding == nil {
		return nil
	}
	return &scores
}

// pickScore chooses the preferred metric, falling back to the other. The
// metric travels with the value so the picker knows whether to mark it.
func pickScore(preferred float64, preferredMetric string, fallback float64, fallbackMetric string) *ModelScore {
	switch {
	case preferred != 0:
		return &ModelScore{Value: preferred, Metric: preferredMetric}
	case fallback != 0:
		return &ModelScore{Value: fallback, Metric: fallbackMetric}
	default:
		return nil
	}
}

// pinnedBenchmarkID is the dataset row San's data names for this model, for one
// whose ID no rule can match. A model is asked for under both spellings: the
// listing's ID, and the free tier's ID with its marker dropped.
func pinnedBenchmarkID(data lineups, provider ProviderID, modelID string) string {
	lineup := data[modelVendorID(provider)]
	for _, id := range []string{strings.ToLower(modelID), canonicalFreeID(modelID)} {
		if spec, ok := lineup.find(id); ok && spec.BenchmarkID != "" {
			return spec.BenchmarkID
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Matching a San model ID to a dataset row
// ---------------------------------------------------------------------------

// benchmarkMatchIndex is the dataset keyed by every spelling a San ID may
// arrive in. It is built once per process, from the data read once per process.
type benchmarkMatchIndex struct {
	// aliases are every spelling of a dataset row: the row's own ID and the
	// slug after its vendor prefix, each normalised.
	aliases map[string][]string
	// bases are those spellings with a trailing version number removed, which
	// is how a dated row answers for the model it dates.
	bases map[string][]string
	// slugs are the bare spellings, searched by prefix for a model listed
	// without the qualifier the dataset adds.
	slugs map[string][]string
}

// benchmarkIndex memoises the index over this process's dataset.
var benchmarkIndex = sync.OnceValue(func() *benchmarkMatchIndex {
	return newBenchmarkMatchIndex(loadBenchmarkData())
})

func newBenchmarkMatchIndex(data benchmarkData) *benchmarkMatchIndex {
	index := &benchmarkMatchIndex{
		aliases: make(map[string][]string, len(data.Models)),
		bases:   make(map[string][]string, len(data.Models)),
		slugs:   make(map[string][]string, len(data.Models)),
	}
	for id := range data.Models {
		full := normalizeBenchmarkID(id)
		index.add(index.aliases, full, id)
		index.add(index.bases, trimVersionSuffix(full), id)
		bare, ok := bareBenchmarkID(id)
		if !ok {
			continue
		}
		alias := normalizeBenchmarkID(bare)
		index.add(index.aliases, alias, id)
		index.add(index.bases, trimVersionSuffix(alias), id)
		index.add(index.slugs, alias, id)
	}
	return index
}

// add files one dataset row under one spelling, without duplicating it.
func (i *benchmarkMatchIndex) add(into map[string][]string, alias, id string) {
	if alias == "" || slices.Contains(into[alias], id) {
		return
	}
	into[alias] = append(into[alias], id)
}

// resolve walks the rules in order and stops at the first that matches at all:
// exact spelling, then the same spelling without a version number, then a
// unique prefix. A match that names more than one model is an answer of "no
// scores" — guessing would put one model's score beside another's name.
func (i *benchmarkMatchIndex) resolve(provider ProviderID, modelID string) string {
	alias := normalizeBenchmarkID(modelID)
	if alias == "" {
		return ""
	}
	if id, matched := pickBenchmarkRow(provider, i.aliases[alias]); matched {
		return id
	}
	if base := trimVersionSuffix(alias); base != "" {
		if id, matched := pickBenchmarkRow(provider, i.bases[base]); matched {
			return id
		}
	}
	id, _ := pickBenchmarkRow(provider, i.prefixed(alias))
	return id
}

// prefixed collects the rows whose bare spelling starts with the alias, for a
// model the dataset lists with a qualifier ("-reasoning", "-vl", a variant).
func (i *benchmarkMatchIndex) prefixed(alias string) []string {
	var out []string
	for slug, ids := range i.slugs {
		if !strings.HasPrefix(slug, alias+"-") {
			continue
		}
		for _, id := range ids {
			if !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// pickBenchmarkRow narrows candidates to one. The bool reports that the
// spelling matched something: a match the provider cannot settle is settled as
// "no scores", not as a reason to keep guessing.
func pickBenchmarkRow(provider ProviderID, candidates []string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	if vendor := benchmarkVendorFor(provider); vendor != "" {
		var own []string
		for _, id := range candidates {
			if benchmarkIDVendor(id) == vendor {
				own = append(own, id)
			}
		}
		if len(own) == 1 {
			return own[0], true
		}
	}
	return "", true
}

// benchmarkIDVendor is the vendor a dataset ID is filed under, or empty.
func benchmarkIDVendor(id string) string {
	vendor, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return strings.ToLower(vendor)
}

// bareBenchmarkID drops the vendor prefix a dataset ID carries.
func bareBenchmarkID(id string) (string, bool) {
	_, bare, ok := strings.Cut(id, "/")
	return bare, ok
}

// normalizeBenchmarkID puts an ID into the shape both sides share: lower case,
// dots and underscores as dashes, no free-tier marker.
func normalizeBenchmarkID(id string) string {
	out := strings.ToLower(strings.TrimSpace(id))
	out = strings.ReplaceAll(out, ".", "-")
	out = strings.ReplaceAll(out, "_", "-")
	return canonicalFreeID(out)
}

// trimVersionSuffix drops a trailing release number — "-1017", "-20251001" —
// so a dated row answers for the undated model. Only the last segment goes:
// "claude-opus-5-5" keeps the 5-5 that names the model.
func trimVersionSuffix(alias string) string {
	cut := strings.LastIndexByte(alias, '-')
	if cut <= 0 {
		return alias
	}
	tail := alias[cut+1:]
	if tail == "" {
		return alias
	}
	for _, r := range tail {
		if r < '0' || r > '9' {
			return alias
		}
	}
	return alias[:cut]
}
