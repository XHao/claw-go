package knowledge

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"log/slog"

	"github.com/XHao/claw-go/memory"
)

// DreamCycle periodically scans recent conversation memory, identifies
// high-frequency topics, and triggers full Map-Reduce distillation for
// topics that have accumulated enough turns since their last distillation.
//
// Inspired by the Complementary Learning Systems (CLS) theory: episodic
// memories are consolidated into semantic knowledge during "offline" periods.
type DreamCycle struct {
	distiller    *Distiller
	mem          *memory.Manager
	store        *ExperienceStore
	log          *slog.Logger
	minFreq      int // minimum keyword frequency to trigger distillation
	lookbackDays int // how many days of memory to scan
}

// NewDreamCycle creates a DreamCycle.
//
//   - distiller:    the Distiller used to run Map-Reduce on identified topics
//   - mem:          the memory Manager to scan for recent TurnSummaries
//   - store:        the ExperienceStore to check last-updated times and save results
//   - minFreq:      minimum keyword frequency threshold (e.g. 3)
//   - lookbackDays: how many days of history to scan (e.g. 7)
func NewDreamCycle(distiller *Distiller, mem *memory.Manager, store *ExperienceStore, minFreq, lookbackDays int) *DreamCycle {
	if minFreq <= 0 {
		minFreq = 3
	}
	if lookbackDays <= 0 {
		lookbackDays = 7
	}
	return &DreamCycle{
		distiller:    distiller,
		mem:          mem,
		store:        store,
		log:          slog.Default(),
		minFreq:      minFreq,
		lookbackDays: lookbackDays,
	}
}

// Run executes one Dream Cycle: scans recent memory, identifies high-frequency
// topics, and distills each qualifying topic. Returns the list of topics that
// were distilled in this run.
func (d *DreamCycle) Run(ctx context.Context) ([]string, error) {
	cutoff := time.Now().Add(-time.Duration(d.lookbackDays) * 24 * time.Hour)

	sessions, err := d.mem.AllSessions()
	if err != nil {
		return nil, err
	}

	// Collect keyword frequencies from recent turns.
	freq := make(map[string]int)
	for _, key := range sessions {
		turns, err := d.mem.ForSession(key).LoadRecent(0)
		if err != nil {
			continue
		}
		for _, t := range turns {
			if t.At.Before(cutoff) {
				continue
			}
			// Use TopicTokens (no synonym expansion) for frequency counting.
			// ExtractKeywords expands synonyms which inflates counts for
			// related-but-unmentioned terms ("docker" → also counts "container",
			// "compose", etc.), producing spurious high-frequency candidates.
			for _, kw := range TopicTokens(t.User) {
				freq[kw]++
			}
		}
	}

	// Identify candidate topics above the frequency threshold.
	type candidate struct {
		topic string
		count int
	}
	var candidates []candidate
	for kw, count := range freq {
		if count >= d.minFreq {
			candidates = append(candidates, candidate{kw, count})
		}
	}
	// Sort by frequency descending for deterministic ordering.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].count > candidates[j].count
	})

	// Distill each candidate, skipping recently updated topics.
	var distilled []string
	for _, c := range candidates {
		if d.recentlyDistilled(c.topic) {
			d.log.Info("dream cycle: skipping recently distilled topic", "topic", c.topic)
			continue
		}
		d.log.Info("dream cycle: distilling topic", "topic", c.topic, "freq", c.count)
		if _, err := d.distiller.Distill(ctx, c.topic, nil); err != nil {
			d.log.Warn("dream cycle: distill failed", "topic", c.topic, "err", err)
			continue
		}
		distilled = append(distilled, c.topic)
	}
	return distilled, nil
}

// recentlyDistilled reports whether the experience file for topic was updated
// within the last 24 hours, indicating it was already distilled recently.
func (d *DreamCycle) recentlyDistilled(topic string) bool {
	info, err := os.Stat(d.store.Path(topic))
	if err != nil {
		return false // file doesn't exist → not recently distilled
	}
	return time.Since(info.ModTime()) < 24*time.Hour
}

// Start runs the Dream Cycle in a background goroutine, executing at the
// given interval until ctx is cancelled. The first execution is delayed by
// one full interval to avoid running at daemon startup.
func (d *DreamCycle) Start(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				topics, err := d.Run(ctx)
				if err != nil {
					d.log.Warn("dream cycle: run failed", "err", err)
					continue
				}
				if len(topics) > 0 {
					d.log.Info("dream cycle: completed", "distilled", strings.Join(topics, ", "))
				} else {
					d.log.Debug("dream cycle: no topics to distill")
				}
			}
		}
	}()
}
