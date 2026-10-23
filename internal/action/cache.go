package action

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mozership/probe-agent/internal/scenario"
)

// cacheResult is res.cache: how the test used its recording.
type cacheResult struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	// Status is miss, hit or partial.
	Status     string `json:"status"`
	Replayed   int    `json:"replayed"`
	DivergedAt int    `json:"diverged_at,omitempty"`
	Reason     string `json:"reason,omitempty"`
	// Stale is whether the recording stopped matching the application.
	Stale bool `json:"stale"`
	// Saved is whether the run was written as the recording.
	Saved bool `json:"saved"`
}

// loadRecording reads the recording at path, which is nil when there is
// none.
func loadRecording(path string) (*scenario.Recording, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	var rec scenario.Recording
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("cache: %s is not a recording: %w", path, err)
	}
	return &rec, nil
}

// saveRecording writes rec to path, for the repository to keep, so that
// it is readable by others as the files of a repository are.
func saveRecording(path string, rec *scenario.Recording) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

// cacheOf returns res.cache for r and o, or nil when the test has no cache.
func cacheOf(r req, o scenario.Outcome, saved bool) *cacheResult {
	if r.Cache == "" {
		return nil
	}
	c := &cacheResult{Path: r.Cache, Mode: r.CacheMode, Status: scenario.ReplayMiss, Saved: saved}
	switch {
	case o.Replay != nil:
		c.Status, c.Replayed, c.DivergedAt, c.Reason, c.Stale = o.Replay.Status, o.Replay.Replayed, o.Replay.DivergedAt, o.Replay.Reason, o.Replay.Stale
	case r.CacheMode == cacheRefresh:
		c.Reason = "cache_mode is refresh"
	default:
		c.Reason = "there is no recording at " + r.Cache
	}
	return c
}
