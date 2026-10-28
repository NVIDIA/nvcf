// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package diskguard keeps the spool from filling up. It pauses preparing
// when free space or the prepare-ahead bound runs out, bounds quarantine by
// size and age, and above the high watermark deletes per policy, oldest
// first. Every deletion is counted, because each one is data that will not
// reach its destination.
package diskguard

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
)

// Metric names. Integration maps them to telemetry instruments.
const (
	CounterSegmentsEvicted   = "segments_evicted"
	CounterQuarantineEvicted = "quarantine_evicted"

	GaugeSpoolUsedBytes   = "spool_used_bytes"
	GaugeSpoolFreeBytes   = "spool_free_bytes"
	GaugePendingSegments  = "pending_segments"
	GaugePendingBytes     = "pending_bytes"
	GaugeOldestPendingAge = "oldest_pending_age_seconds"
)

// Usage is the size of the spool volume.
type Usage struct {
	Total uint64
	Free  uint64
}

// Volume reports the spool volume's size.
type Volume interface {
	Usage() (Usage, error)
}

// Candidate is one thing the guard may delete, or one pending segment for
// the gauges.
type Candidate struct {
	Key     string
	Bytes   int64
	ModTime time.Time
}

// QuarantineStore lists and removes quarantined entries. *Quarantine
// implements it.
type QuarantineStore interface {
	Entries() ([]Candidate, error)
	Remove(key string) error
}

// Segments lists closed segments that may be evicted and evicts one: its
// source and transports are deleted and the journal records the loss. The
// list must never include the highest-index segment of a prefix. Evict wraps
// ErrDeletedNotSynced when the data is gone but a directory sync failed.
type Segments interface {
	Evictable() ([]Candidate, error)
	Evict(key string) error
}

// Counter receives event counts.
type Counter interface {
	Add(name string, delta int64)
}

// Gauges receives the spool gauges.
type Gauges interface {
	Set(name string, value float64)
}

// PauseReason says why preparing is paused.
type PauseReason string

const (
	PauseNone          PauseReason = ""
	PauseFreeSpace     PauseReason = "free_space"
	PausePrepareAhead  PauseReason = "prepare_ahead"
	PauseHighWatermark PauseReason = "high_watermark"
)

// Decision is whether the next segment may be prepared.
type Decision struct {
	Allowed bool
	Reason  PauseReason
}

// Report is what one Enforce pass did.
type Report struct {
	AboveWatermark    bool
	QuarantineEvicted int
	SegmentsEvicted   int
	FreedBytes        int64
}

// Options holds the guard's collaborators. Zero values are safe.
type Options struct {
	Counter Counter
	Gauges  Gauges
	Logger  *slog.Logger
}

// Guard applies the disk rules. Enforce must not run concurrently with
// itself; the other methods are safe to call at any time.
type Guard struct {
	cfg        Config
	volume     Volume
	quarantine QuarantineStore
	segments   Segments
	counter    Counter
	gauges     Gauges
	logger     *slog.Logger
}

// New builds a guard. cfg must already be resolved against the batch size.
func New(cfg Config, volume Volume, quarantine QuarantineStore, segments Segments, opts Options) (*Guard, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	g := &Guard{
		cfg:        cfg,
		volume:     volume,
		quarantine: quarantine,
		segments:   segments,
		counter:    opts.Counter,
		gauges:     opts.Gauges,
		logger:     opts.Logger,
	}
	if g.counter == nil {
		g.counter = discard{}
	}
	if g.gauges == nil {
		g.gauges = discard{}
	}
	if g.logger == nil {
		g.logger = slog.Default()
	}
	for _, name := range []string{CounterSegmentsEvicted, CounterQuarantineEvicted} {
		g.counter.Add(name, 0)
	}
	return g, nil
}

// AllowPrepare reports whether one more segment, whose transport is
// estimated at estimate bytes, may be prepared while waiting segments are
// already prepared and not yet in a submitted batch. A pause only stops
// preparing; submitting what is prepared goes on.
func (g *Guard) AllowPrepare(estimate int64, waiting int) (Decision, error) {
	if waiting >= g.cfg.PrepareAhead {
		return Decision{Reason: PausePrepareAhead}, nil
	}
	usage, err := g.volume.Usage()
	if err != nil {
		return Decision{}, err
	}
	if g.aboveWatermark(usedBytes(usage), usage.Total) {
		return Decision{Reason: PauseHighWatermark}, nil
	}
	if int64(usage.Free) < max(2*estimate, g.cfg.MinFreeBytes) {
		return Decision{Reason: PauseFreeSpace}, nil
	}
	return Decision{Allowed: true}, nil
}

// Enforce first bounds quarantine: entries older than QuarantineMaxAge go,
// then the oldest until quarantine fits its share of the spool. Then, above
// the high watermark, it deletes per Policy, oldest first, until the spool
// is back at the watermark. Every deletion is counted.
func (g *Guard) Enforce(now time.Time) (Report, error) {
	var report Report
	usage, err := g.volume.Usage()
	if err != nil {
		return report, err
	}
	entries, err := g.quarantine.Entries()
	if err != nil {
		return report, err
	}
	sortOldestFirst(entries)

	var errs []error
	limit := int64(g.cfg.QuarantineMaxFraction * float64(usage.Total))
	var kept []Candidate
	var held int64
	for _, e := range entries {
		if now.Sub(e.ModTime) > g.cfg.QuarantineMaxAge {
			errs = append(errs, g.removeQuarantine(e, &report))
			continue
		}
		kept = append(kept, e)
		held += e.Bytes
	}
	for len(kept) > 0 && held > limit {
		errs = append(errs, g.removeQuarantine(kept[0], &report))
		held -= kept[0].Bytes
		kept = kept[1:]
	}

	used := usedBytes(usage) - report.FreedBytes
	if g.aboveWatermark(used, usage.Total) {
		report.AboveWatermark = true
		errs = append(errs, g.relieve(used-watermarkBytes(g.cfg.HighWatermark, usage.Total), kept, &report))
	}
	if report.QuarantineEvicted > 0 || report.SegmentsEvicted > 0 {
		g.logger.Warn("deleted spool data to stay within disk bounds",
			"quarantine_evicted", report.QuarantineEvicted,
			"segments_evicted", report.SegmentsEvicted,
			"freed_bytes", report.FreedBytes)
	}
	return report, errors.Join(errs...)
}

// relieve frees need bytes per the policy: quarantine first, then the oldest
// closed segments.
func (g *Guard) relieve(need int64, quarantine []Candidate, report *Report) error {
	if g.cfg.Policy == PolicyNone {
		g.logger.Warn("spool above the high watermark; preparing paused and nothing deleted by policy")
		return nil
	}
	freed := int64(0)
	var errs []error
	if g.cfg.Policy == PolicyQuarantineThenOldest {
		for _, e := range quarantine {
			if freed >= need {
				break
			}
			before := report.FreedBytes
			errs = append(errs, g.removeQuarantine(e, report))
			freed += report.FreedBytes - before
		}
	}
	if freed < need {
		segments, err := g.segments.Evictable()
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		sortOldestFirst(segments)
		for _, s := range segments {
			if freed >= need {
				break
			}
			if err := g.segments.Evict(s.Key); err != nil {
				errs = append(errs, fmt.Errorf("evict segment: %w", err))
				if !errors.Is(err, ErrDeletedNotSynced) {
					continue
				}
			}
			g.counter.Add(CounterSegmentsEvicted, 1)
			report.SegmentsEvicted++
			report.FreedBytes += s.Bytes
			freed += s.Bytes
		}
	}
	if freed < need {
		g.logger.Error("spool still above the high watermark after deleting everything the policy allows",
			"still_needed_bytes", need-freed)
	}
	return errors.Join(errs...)
}

// removeQuarantine deletes one entry. A deletion whose directory sync failed
// still happened, so it is counted and its error returned.
func (g *Guard) removeQuarantine(e Candidate, report *Report) error {
	err := g.quarantine.Remove(e.Key)
	if err != nil && !errors.Is(err, ErrDeletedNotSynced) {
		return fmt.Errorf("evict quarantine entry: %w", err)
	}
	g.counter.Add(CounterQuarantineEvicted, 1)
	report.QuarantineEvicted++
	report.FreedBytes += e.Bytes
	if err != nil {
		return fmt.Errorf("evict quarantine entry: %w", err)
	}
	return nil
}

// UpdateGauges sets the spool gauges from the volume and the segments not
// yet done.
func (g *Guard) UpdateGauges(now time.Time, pending []Candidate) error {
	usage, err := g.volume.Usage()
	if err != nil {
		return err
	}
	var bytes int64
	var oldest time.Time
	for _, p := range pending {
		bytes += p.Bytes
		if oldest.IsZero() || p.ModTime.Before(oldest) {
			oldest = p.ModTime
		}
	}
	age := 0.0
	if !oldest.IsZero() {
		age = max(now.Sub(oldest).Seconds(), 0)
	}
	g.gauges.Set(GaugeSpoolUsedBytes, float64(usedBytes(usage)))
	g.gauges.Set(GaugeSpoolFreeBytes, float64(usage.Free))
	g.gauges.Set(GaugePendingSegments, float64(len(pending)))
	g.gauges.Set(GaugePendingBytes, float64(bytes))
	g.gauges.Set(GaugeOldestPendingAge, age)
	return nil
}

func (g *Guard) aboveWatermark(used int64, total uint64) bool {
	return used > watermarkBytes(g.cfg.HighWatermark, total)
}

func watermarkBytes(fraction float64, total uint64) int64 {
	return int64(fraction * float64(total))
}

func usedBytes(u Usage) int64 {
	if u.Free > u.Total {
		return 0
	}
	return int64(u.Total - u.Free)
}

func sortOldestFirst(items []Candidate) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].ModTime.Before(items[j].ModTime) })
}

type discard struct{}

func (discard) Add(string, int64)   {}
func (discard) Set(string, float64) {}
