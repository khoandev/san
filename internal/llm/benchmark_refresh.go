package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Keeping the benchmark scores current: llmbenchmark.ai, fetched weekly and cut
// down to the four numbers San draws.
//
// The download is the whole dataset (about 21 MB), so it runs off the startup
// path, behind the same daily-refresh switch as the models.dev data, and lands
// in ~/.san/cache/benchmarks.json. Scores change far more slowly than lineups,
// hence the week rather than the day.

const (
	benchmarkDatasetURL = "https://llmbenchmark.ai/data/dataset.json"

	// benchmarkDataMaxAge is how long a fetched copy stays current.
	benchmarkDataMaxAge = 7 * 24 * time.Hour

	// benchmarkDataTimeout bounds one fetch. It runs off the startup path, so
	// a slow network delays the next start rather than this one.
	benchmarkDataTimeout = 60 * time.Second

	// benchmarkDataMaxBytes caps the download. The dataset is ~21 MB; a body
	// far past that is not the dataset San expects.
	benchmarkDataMaxBytes = 64 << 20
)

// RefreshBenchmarkData fetches the dataset into the cache when the cached copy
// is missing or older than a week. The new scores take effect on the next
// start, like the models.dev data.
func RefreshBenchmarkData(ctx context.Context) error {
	return refreshBenchmarkData(ctx, benchmarkDatasetURL, benchmarkDataCachePath(), time.Now())
}

// refreshBenchmarkData is RefreshBenchmarkData with the URL, path and clock
// supplied, so a test can exercise the freshness rule without the network.
func refreshBenchmarkData(ctx context.Context, url, path string, now time.Time) error {
	if path == "" {
		return nil
	}
	if info, err := os.Stat(path); err == nil && now.Sub(info.ModTime()) < benchmarkDataMaxAge {
		return nil
	}
	data, err := fetchBenchmarkDataset(ctx, url)
	if err != nil {
		return err
	}
	return writeBenchmarkData(path, data)
}

// fetchBenchmarkDataset downloads the dataset and keeps San's share of it.
func fetchBenchmarkDataset(ctx context.Context, url string) (benchmarkData, error) {
	ctx, cancel := context.WithTimeout(ctx, benchmarkDataTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return benchmarkData{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return benchmarkData{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return benchmarkData{}, fmt.Errorf("llm: llmbenchmark answered http %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, benchmarkDataMaxBytes+1))
	if err != nil {
		return benchmarkData{}, err
	}
	if len(raw) > benchmarkDataMaxBytes {
		return benchmarkData{}, fmt.Errorf("llm: llmbenchmark dataset is larger than %d bytes", benchmarkDataMaxBytes)
	}
	data, err := trimBenchmarkDataset(raw)
	if err != nil {
		return benchmarkData{}, fmt.Errorf("llm: reading the llmbenchmark dataset: %w", err)
	}
	// A body that parses but carries none of the four metrics means the
	// dataset moved under San, not that every model lost its scores. Refusing
	// it keeps the last good copy rather than blanking the column for a week.
	if len(data.Models) == 0 {
		return benchmarkData{}, fmt.Errorf("llm: the llmbenchmark dataset carried none of the metrics San reads")
	}
	return data, nil
}
