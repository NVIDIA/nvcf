// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package diskguard_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/diskguard"
)

var now = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type fakeVolume struct{ usage diskguard.Usage }

func (v *fakeVolume) Usage() (diskguard.Usage, error) { return v.usage, nil }

// fakeStore serves both quarantine and segments, and records removals in
// order.
type fakeStore struct {
	kind     string
	items    []diskguard.Candidate
	removed  *[]string
	unsynced map[string]bool
}

func (s *fakeStore) Entries() ([]diskguard.Candidate, error)   { return s.items, nil }
func (s *fakeStore) Evictable() ([]diskguard.Candidate, error) { return s.items, nil }
func (s *fakeStore) Remove(key string) error                   { return s.drop(key) }
func (s *fakeStore) Evict(key string) error                    { return s.drop(key) }

func (s *fakeStore) drop(key string) error {
	for i, item := range s.items {
		if item.Key == key {
			s.items = append(s.items[:i], s.items[i+1:]...)
			*s.removed = append(*s.removed, s.kind+":"+key)
			if s.unsynced[key] {
				return fmt.Errorf("remove %s: %w", key, diskguard.ErrDeletedNotSynced)
			}
			return nil
		}
	}
	return errors.New("unknown key")
}

type metrics struct {
	counts map[string]int64
	gauges map[string]float64
}

func newMetrics() *metrics {
	return &metrics{counts: map[string]int64{}, gauges: map[string]float64{}}
}

func (m *metrics) Add(name string, delta int64)   { m.counts[name] += delta }
func (m *metrics) Set(name string, value float64) { m.gauges[name] = value }

func item(key string, bytes int64, age time.Duration) diskguard.Candidate {
	return diskguard.Candidate{Key: key, Bytes: bytes, ModTime: now.Add(-age)}
}

type world struct {
	volume     *fakeVolume
	quarantine *fakeStore
	segments   *fakeStore
	removed    []string
	metrics    *metrics
}

func newWorld(total, free uint64) *world {
	w := &world{volume: &fakeVolume{usage: diskguard.Usage{Total: total, Free: free}}, metrics: newMetrics()}
	w.quarantine = &fakeStore{kind: "quarantine", removed: &w.removed}
	w.segments = &fakeStore{kind: "segment", removed: &w.removed}
	return w
}

func (w *world) guard(t *testing.T, cfg diskguard.Config) *diskguard.Guard {
	t.Helper()
	g, err := diskguard.New(cfg, w.volume, w.quarantine, w.segments, diskguard.Options{Counter: w.metrics, Gauges: w.metrics})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestConfigDefaultsAndValidation(t *testing.T) {
	defaults := diskguard.DefaultConfig()
	if err := defaults.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if defaults.Policy != diskguard.PolicyQuarantineThenOldest || defaults.HighWatermark != 0.85 ||
		defaults.QuarantineMaxFraction != 0.2 || defaults.QuarantineMaxAge != 7*24*time.Hour || defaults.PrepareAhead != 16 {
		t.Fatalf("defaults = %+v, want the proposed values", defaults)
	}
	cases := []struct {
		name   string
		mutate func(*diskguard.Config)
	}{
		{"negative free floor", func(c *diskguard.Config) { c.MinFreeBytes = -1 }},
		{"zero prepare ahead", func(c *diskguard.Config) { c.PrepareAhead = 0 }},
		{"watermark zero", func(c *diskguard.Config) { c.HighWatermark = 0 }},
		{"watermark one", func(c *diskguard.Config) { c.HighWatermark = 1 }},
		{"unknown policy", func(c *diskguard.Config) { c.Policy = "evict-newest" }},
		{"empty policy", func(c *diskguard.Config) { c.Policy = "" }},
		{"quarantine fraction zero", func(c *diskguard.Config) { c.QuarantineMaxFraction = 0 }},
		{"quarantine fraction above one", func(c *diskguard.Config) { c.QuarantineMaxFraction = 1.5 }},
		{"quarantine age zero", func(c *diskguard.Config) { c.QuarantineMaxAge = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if _, err := diskguard.New(cfg, &fakeVolume{}, &fakeStore{}, &fakeStore{}, diskguard.Options{}); err == nil {
				t.Fatalf("New accepted %s", tc.name)
			}
		})
	}
}

func TestLowPrepareAheadIsRaisedToTheBatchSize(t *testing.T) {
	cfg := diskguard.DefaultConfig()
	cfg.PrepareAhead = 4
	resolved, warnings := cfg.Resolve(16)
	if resolved.PrepareAhead != 16 || len(warnings) != 1 || !strings.Contains(warnings[0], "16") {
		t.Fatalf("Resolve = %d with %q, want 16 and one warning", resolved.PrepareAhead, warnings)
	}
	cfg.PrepareAhead = 32
	if resolved, warnings := cfg.Resolve(16); resolved.PrepareAhead != 32 || len(warnings) != 0 {
		t.Fatalf("Resolve kept %d with %q, want 32 unchanged", resolved.PrepareAhead, warnings)
	}
}

func TestAllowPrepare(t *testing.T) {
	const mib = uint64(1) << 20
	cases := []struct {
		name        string
		total, free uint64
		estimate    int64
		waiting     int
		want        diskguard.PauseReason
	}{
		{"room to prepare", 4096 * mib, 2048 * mib, 30 << 20, 3, diskguard.PauseNone},
		{"below the floor", 2048 * mib, 500 * mib, 30 << 20, 0, diskguard.PauseFreeSpace},
		{"twice the estimate is above the floor", 2048 * mib, 900 * mib, 600 << 20, 0, diskguard.PauseFreeSpace},
		{"prepare ahead is full", 4096 * mib, 2048 * mib, 30 << 20, 16, diskguard.PausePrepareAhead},
		{"above the high watermark", 100 * 1024 * mib, 10 * 1024 * mib, 1, 0, diskguard.PauseHighWatermark},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(tc.total, tc.free)
			got, err := w.guard(t, diskguard.DefaultConfig()).AllowPrepare(tc.estimate, tc.waiting)
			if err != nil {
				t.Fatalf("AllowPrepare: %v", err)
			}
			if got.Reason != tc.want || got.Allowed != (tc.want == diskguard.PauseNone) {
				t.Fatalf("AllowPrepare = %+v, want reason %q", got, tc.want)
			}
		})
	}
}

func TestQuarantineOlderThanTheMaxAgeIsEvicted(t *testing.T) {
	w := newWorld(1_000_000, 900_000)
	w.quarantine.items = []diskguard.Candidate{item("fresh", 10, 24*time.Hour), item("stale", 10, 8*24*time.Hour)}

	report, err := w.guard(t, diskguard.DefaultConfig()).Enforce(now)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if strings.Join(w.removed, ",") != "quarantine:stale" || report.QuarantineEvicted != 1 {
		t.Fatalf("removed %v (report %+v), want only the stale entry", w.removed, report)
	}
	if w.metrics.counts[diskguard.CounterQuarantineEvicted] != 1 {
		t.Fatalf("%s = %d, want 1", diskguard.CounterQuarantineEvicted, w.metrics.counts[diskguard.CounterQuarantineEvicted])
	}
}

func TestQuarantineAboveItsShareOfTheSpoolIsEvictedOldestFirst(t *testing.T) {
	// The spool is 1000 bytes, so quarantine may hold 200.
	w := newWorld(1000, 700)
	w.quarantine.items = []diskguard.Candidate{
		item("newest", 100, time.Hour),
		item("oldest", 150, 3*time.Hour),
		item("rejects/seg-1.jsonl.gz", 50, 2*time.Hour),
	}

	report, err := w.guard(t, diskguard.DefaultConfig()).Enforce(now)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if strings.Join(w.removed, ",") != "quarantine:oldest" || report.FreedBytes != 150 {
		t.Fatalf("removed %v (report %+v), want only the oldest entry", w.removed, report)
	}
}

func TestAboveTheWatermarkQuarantineGoesFirstThenTheOldestSegments(t *testing.T) {
	// F25: 950 of 1000 bytes used; the watermark is 850, so 100 must go.
	w := newWorld(1000, 50)
	w.quarantine.items = []diskguard.Candidate{item("q-new", 40, time.Hour), item("q-old", 30, 2*time.Hour)}
	w.segments.items = []diskguard.Candidate{item("s-mid", 60, 4*time.Hour), item("s-old", 50, 5*time.Hour), item("s-new", 70, 3*time.Hour)}

	report, err := w.guard(t, diskguard.DefaultConfig()).Enforce(now)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got := strings.Join(w.removed, ","); got != "quarantine:q-old,quarantine:q-new,segment:s-old" {
		t.Fatalf("eviction order = %s, want quarantine oldest first, then the oldest segment", got)
	}
	if !report.AboveWatermark || report.QuarantineEvicted != 2 || report.SegmentsEvicted != 1 || report.FreedBytes != 120 {
		t.Fatalf("report = %+v, want 2 quarantine and 1 segment evicted, 120 bytes", report)
	}
	if w.metrics.counts[diskguard.CounterSegmentsEvicted] != 1 || w.metrics.counts[diskguard.CounterQuarantineEvicted] != 2 {
		t.Fatalf("counts = %v, want every eviction counted", w.metrics.counts)
	}
}

func TestOldestPolicyKeepsQuarantine(t *testing.T) {
	w := newWorld(1000, 50)
	w.quarantine.items = []diskguard.Candidate{item("q", 40, time.Hour)}
	w.segments.items = []diskguard.Candidate{item("s-new", 70, time.Hour), item("s-old", 60, 2*time.Hour)}
	cfg := diskguard.DefaultConfig()
	cfg.Policy = diskguard.PolicyOldest

	if _, err := w.guard(t, cfg).Enforce(now); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got := strings.Join(w.removed, ","); got != "segment:s-old,segment:s-new" {
		t.Fatalf("eviction order = %s, want segments only, oldest first", got)
	}
}

func TestNonePolicyOnlyPauses(t *testing.T) {
	w := newWorld(1000, 50)
	w.quarantine.items = []diskguard.Candidate{item("q", 40, time.Hour)}
	w.segments.items = []diskguard.Candidate{item("s", 70, time.Hour)}
	cfg := diskguard.DefaultConfig()
	cfg.Policy = diskguard.PolicyNone
	g := w.guard(t, cfg)

	report, err := g.Enforce(now)
	if err != nil || !report.AboveWatermark || len(w.removed) != 0 {
		t.Fatalf("Enforce = %+v, %v, removed %v; want above the watermark with nothing deleted", report, err, w.removed)
	}
	if d, _ := g.AllowPrepare(1, 0); d.Reason != diskguard.PauseHighWatermark {
		t.Fatalf("AllowPrepare = %+v, want paused at the high watermark", d)
	}
}

func TestBelowTheWatermarkNothingIsEvicted(t *testing.T) {
	w := newWorld(1000, 200)
	w.segments.items = []diskguard.Candidate{item("s", 70, time.Hour)}
	report, err := w.guard(t, diskguard.DefaultConfig()).Enforce(now)
	if err != nil || report.AboveWatermark || len(w.removed) != 0 {
		t.Fatalf("Enforce = %+v, %v, removed %v; want nothing", report, err, w.removed)
	}
}

func TestGaugesFollowTheSpool(t *testing.T) {
	w := newWorld(1000, 400)
	g := w.guard(t, diskguard.DefaultConfig())
	pending := []diskguard.Candidate{item("a", 100, 10*time.Minute), item("b", 50, time.Hour)}

	if err := g.UpdateGauges(now, pending); err != nil {
		t.Fatalf("UpdateGauges: %v", err)
	}
	want := map[string]float64{
		diskguard.GaugeSpoolUsedBytes:   600,
		diskguard.GaugeSpoolFreeBytes:   400,
		diskguard.GaugePendingSegments:  2,
		diskguard.GaugePendingBytes:     150,
		diskguard.GaugeOldestPendingAge: 3600,
	}
	for name, value := range want {
		if w.metrics.gauges[name] != value {
			t.Errorf("%s = %v, want %v", name, w.metrics.gauges[name], value)
		}
	}

	w.volume.usage.Free = 550
	if err := g.UpdateGauges(now, pending[:1]); err != nil {
		t.Fatalf("UpdateGauges: %v", err)
	}
	if w.metrics.gauges[diskguard.GaugePendingSegments] != 1 || w.metrics.gauges[diskguard.GaugeOldestPendingAge] != 600 ||
		w.metrics.gauges[diskguard.GaugeSpoolUsedBytes] != 450 {
		t.Fatalf("gauges after a segment left = %v", w.metrics.gauges)
	}
	if err := g.UpdateGauges(now, nil); err != nil || w.metrics.gauges[diskguard.GaugeOldestPendingAge] != 0 {
		t.Fatalf("empty spool age = %v, %v; want 0", w.metrics.gauges[diskguard.GaugeOldestPendingAge], err)
	}
}

func TestQuarantineDirectory(t *testing.T) {
	spool := t.TempDir()
	q := diskguard.NewQuarantine(filepath.Join(spool, ".rtu-quarantine"))
	source := filepath.Join(spool, "request-trace.000003.jsonl.gz")
	transport := filepath.Join(spool, "seg-3.parquet")
	for path, size := range map[string]int{source: 40, transport: 20} {
		if err := os.WriteFile(path, make([]byte, size), 0o640); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := q.Admit("seg-3", source, transport); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("quarantined source left in the spool")
	}
	if err := os.MkdirAll(q.RejectsDir(), 0o750); err != nil {
		t.Fatalf("create rejects dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(q.RejectsDir(), "seg-1.jsonl.gz"), make([]byte, 7), 0o640); err != nil {
		t.Fatalf("write reject artifact: %v", err)
	}

	entries, err := q.Entries()
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	if len(entries) != 2 || entries[0].Key != "rejects/seg-1.jsonl.gz" || entries[0].Bytes != 7 ||
		entries[1].Key != "seg-3" || entries[1].Bytes != 60 || entries[1].ModTime.IsZero() {
		t.Fatalf("entries = %+v, want the reject artifact (7 bytes) and seg-3 (60 bytes)", entries)
	}

	for _, bad := range []string{"../escape", "rejects/../../escape", "", "rejects", "a/b/c"} {
		if err := q.Remove(bad); err == nil {
			t.Fatalf("Remove accepted %q", bad)
		}
	}
	for _, e := range entries {
		if err := q.Remove(e.Key); err != nil {
			t.Fatalf("Remove %s: %v", e.Key, err)
		}
	}
	if entries, err := q.Entries(); err != nil || len(entries) != 0 {
		t.Fatalf("entries after removal = %v, %v; want none", entries, err)
	}
	if err := q.Admit("../escape", transport); err == nil {
		t.Fatal("Admit accepted an id that escapes the quarantine")
	}
}

func TestQuarantineThatDoesNotExistYetIsEmpty(t *testing.T) {
	q := diskguard.NewQuarantine(filepath.Join(t.TempDir(), "missing"))
	if entries, err := q.Entries(); err != nil || len(entries) != 0 {
		t.Fatalf("Entries = %v, %v; want none", entries, err)
	}
}

func TestStatVolumeReportsTheSpool(t *testing.T) {
	usage, err := diskguard.StatVolume(t.TempDir()).Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Total == 0 || usage.Free > usage.Total {
		t.Fatalf("usage = %+v, want a total with free space within it", usage)
	}
}

func TestADeletionWhoseSyncFailedIsStillCounted(t *testing.T) {
	w := newWorld(1000, 50)
	w.quarantine.items = []diskguard.Candidate{item("q-old", 30, 2*time.Hour)}
	w.quarantine.unsynced = map[string]bool{"q-old": true}
	w.segments.items = []diskguard.Candidate{item("s-old", 80, 5*time.Hour)}
	w.segments.unsynced = map[string]bool{"s-old": true}

	report, err := w.guard(t, diskguard.DefaultConfig()).Enforce(now)
	if !errors.Is(err, diskguard.ErrDeletedNotSynced) {
		t.Fatalf("Enforce error = %v, want the sync failure reported", err)
	}
	if report.QuarantineEvicted != 1 || report.SegmentsEvicted != 1 || report.FreedBytes != 110 {
		t.Fatalf("report = %+v, want both deletions counted", report)
	}
	if w.metrics.counts[diskguard.CounterQuarantineEvicted] != 1 || w.metrics.counts[diskguard.CounterSegmentsEvicted] != 1 {
		t.Fatalf("counts = %v, want both deletions counted", w.metrics.counts)
	}
}
