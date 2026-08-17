package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
)

// probeCacheTTL is how long a recorded probe result is trusted before
// `gummi doctor --deep` re-probes. A compiled-in constant (no config
// knob); it keeps back-to-back deep runs cheap without ever going stale
// for a session's worth of setup.
const probeCacheTTL = 6 * time.Hour

// probeCacheFile is the workspace sidecar recording the last-known-good
// (or known-bad) probe result per backend|model. It is a plain JSON map,
// written crash-safely via atomicfile, and is deliberately separate from
// the feature store: the probe cache is never part of a feature's audit
// trail and needs no schema migration.
const probeCacheFile = "model-probe-cache.json"

// probeCacheEntry is one cached probe result: whether the model was
// servable and when it was probed (ProbedAt drives the TTL check).
type probeCacheEntry struct {
	OK       bool      `json:"ok"`
	ProbedAt time.Time `json:"probed_at"`
}

// probeCacheKey keys a probe result by backend and model — "backend|model"
// — so two roles that resolve to the same model on the same backend share
// one cache entry and one live probe.
func probeCacheKey(backend, model string) string { return backend + "|" + model }

// loadProbeCache reads the sidecar into a map. It is fallible and never
// blocks the report: a missing, unreadable, or corrupt file degrades to a
// live probe on every --deep run.
func loadProbeCache(path string) (map[string]probeCacheEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]probeCacheEntry{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// recordProbe writes (or updates) one key in the sidecar, merging with any
// existing entries and writing the whole map crash-safely. now is injected
// so tests stay deterministic. Best-effort: a failed write never blocks the
// report — the probe result is still reported for this run.
func recordProbe(path, key string, ok bool, now time.Time) error {
	m, err := loadProbeCache(path)
	if err != nil {
		m = map[string]probeCacheEntry{}
	}
	m[key] = probeCacheEntry{OK: ok, ProbedAt: now}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o644)
}
