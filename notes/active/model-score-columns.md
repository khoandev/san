# Model score columns (and a clean free-tier name)

Status: active (implementation complete, uncommitted)

## Goal

The Models tab of `/models` names an OpenCode Zen free model by the model
underneath it — no "Free" marker — and every model row carries two score
columns from llmbenchmark.ai: `◈` intelligence and `⌘` coding. The dataset is
fetched in the background once a week and cached, so the columns work offline
and never delay a start.

## Decisions

- The free-tier marker comes off the display name only; the ID keeps it, because
  `opencodeFreeModel` gates the free tier on the ID. `san.json` no longer states
  a marker in the first place, and `llm.ModelDisplayName` /
  `llm.CleanModelDisplayName` resolve the name through San's data — first the
  provider's own vendor, then every vendor — so a listing cached before the data
  was corrected still renders cleanly. The answer is kept in
  `~/.san/cache/model-names.json` (no TTL, atomic write): the picker asks on
  every render and a name is derivable, not volatile.
- The read side cleans too, not just `ListModels`: `Store.CachedModelDisplayName`
  (status bar) and the picker's `newProviderModelItem` both route through the
  helper, so no surface depends on a refresh having happened.
- `~/.san/cache/benchmarks.json` is San's own file, never `model-scores.json`
  (an earlier experiment's file). It is the dataset trimmed to four metrics —
  `aa_intelligence_index`, `aa_coding_index`, `arena_text_elo`,
  `arena_text_coding_elo` — one observation per model and metric, latest
  `observedAt` wins; models with none of them are dropped. `fetchedAt` is the
  newest observation date, not the download time.
- Refresh is weekly (`benchmarkDataMaxAge`), on the startup path's background
  command beside the models.dev refresh, under the same
  `SAN_DISABLE_MODEL_REFRESH` switch. The download is bounded: 60 s timeout,
  64 MB cap, and a payload that parses but carries none of the four metrics is
  refused so the last good cache survives instead of being blanked for a week.
  Failures log at debug and leave the old file alone; a missing or damaged file
  reads as an empty set, never an error.
- Matching a San ID to a dataset row normalises both sides — lower case, dots
  and underscores as dashes, `-free` / `:free` stripped — then tries: exact
  spelling, the same spelling with a trailing release number removed
  (`-1017`, `-20251001`), and a unique prefix. Candidates are narrowed by the
  provider's dataset vendor (`bigmodel`→`z-ai`, `mimo`→`xiaomi`,
  `moonshot`→`moonshotai`, `alibaba`→`qwen`, `minmax`→`minimax`; Zen has no
  vendor and takes no preference). A match that names more than one model is
  "no scores" rather than a guess.
- A model whose ID cannot be matched at all can be pinned in `san.json` with
  `benchmark_id`; `beam-free` is pinned to `reflection/beam-1.0-501b-a23b`.
  `big-pickle` and `flint-40b-free` have no upstream row and stay blank.
- The pair travels on `ModelInfo` as `Scores *ModelScores` (JSON `omitempty`),
  so the 24 h listing cache written by an older San still decodes. AA's index
  wins per column, the Arena rank is the fallback, and each score carries the
  metric it came from, which is how the UI tells an Elo from an index.
- The row is a grid: icon, five dots (`score / 20` for an index,
  `(elo - 1000) / 110` for a rank, clamped to 0–5), and the number on its own
  scale — `~1463` for an Elo. A model the dataset never measured shows `--`,
  not a zero. Name and window columns stay fixed; when the panel is at its
  narrowest the gaps tighten from two columns to one so the two score columns
  and a 12-column name still fit, and the trailing labels are the part that
  goes without room. The legend under the search box names the columns and
  appears on the Models tab only.

## Files of record

- `internal/llm/model_names.go` — marker stripping, data lookup, the persistent
  name cache.
- `internal/llm/benchmark_data.go`, `internal/llm/benchmark_refresh.go` — the
  trimmed dataset, the cache file, the weekly refresh.
- `internal/llm/benchmark_scores.go` — ID normalisation, the alias index, the
  provider preference, `benchmark_id`, and `ModelScoresFor`.
- `internal/llm/provider.go`, `internal/llm/vendor.go`,
  `internal/llm/vendor_data.go`, `internal/llm/vendor_convert.go`,
  `internal/llm/data/san.json` — `Scores` on `ModelInfo`, the listing path, and
  the two hand-kept fields (`name` cleanup, `benchmark_id`).
- `internal/app/model_data.go` — the background refresh, beside models.dev.
- `internal/app/input/on_provider.go`, `on_provider_data.go`,
  `on_provider_view.go` — the row's scores and the grid that holds them.

## Verification

- `go test ./internal/llm` covers the trim (latest observation wins, unknown
  metrics dropped, no metrics at all refused), the cache round-trip, damaged
  and missing files, the freshness short-circuit, a failed refresh keeping the
  old file, `model-scores.json` left untouched, the ten ID-matching rules
  including the ambiguous case, the `benchmark_id` pin, AA-over-Elo, and the
  listing agreeing with `ModelScoresFor`.
- `go test ./internal/app/input` covers the rendered cells (`◈●●●○○ 59.1`,
  `⌘●●●●○ 78`, `~1463`, `--`), one grid across rows for the name, both scores
  and the labels, rows fitting the panel's inner width at 60/80/120 columns,
  the legend appearing on the Models tab only, and the search filter ignoring
  scores.
- `gofmt -l`, `go vet` and `go build ./...` are clean; the model list and the
  status bar are clean of the free-tier marker on a cached listing too, which
  `TestFreeModelRowKeepsTheIDAndDropsTheMarker` and
  `TestEmbeddedFreeModelNamesCarryNoMarker` pin down.
