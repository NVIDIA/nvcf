// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/foreign"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// counterFor maps the event counters of the journal and cleanup packages to
// telemetry instruments. Events with no instrument are dropped.
func counterFor(m telemetry.Metrics) eventCounter { return eventCounter{m: m} }

type eventCounter struct{ m telemetry.Metrics }

func (c eventCounter) Add(name string, delta int64) {
	switch name {
	case cleanup.CounterSegmentsDone:
		c.m.Add(telemetry.Deletions, float64(delta), telemetry.Labels{"status": "success"})
	case cleanup.CounterSourceChanged:
		c.m.Add(telemetry.Deletions, float64(delta), telemetry.Labels{"status": "failure"})
	case journalfs.CounterBatchesAmbiguous:
		c.m.Add(telemetry.SubmitsAmbiguous, float64(delta), nil)
	}
}

type batchMetrics struct{ m telemetry.Metrics }

func (b batchMetrics) Observe(name string, value float64) {
	switch name {
	case batch.MetricBatchSegments:
		b.m.Observe(telemetry.BatchSegments, value, nil)
	case batch.MetricBatchBytes:
		b.m.Observe(telemetry.BatchBytes, value, nil)
	}
}

// batchJournal records batches for the batch package.
type batchJournal struct {
	store *journalfs.Store
	point func(string)
}

func (j batchJournal) OpenBatch(b batch.Batch, backendName string) error {
	members := make([]journal.Member, 0, len(b.Members))
	for _, m := range b.Members {
		members = append(members, journal.Member{SegmentID: m.SegmentID, Path: m.File.Path})
	}
	if err := j.store.CreateBatch(journal.Batch{ID: b.ID, Backend: backendName, Destination: b.Destination, Members: members}); err != nil {
		return err
	}
	j.point(PointBatchRecorded)
	return nil
}

func (j batchJournal) SaveReceipt(batchID, backendName, receiptID string) error {
	if err := j.store.SetReceipt(batchID, journal.Receipt{Backend: backendName, ID: receiptID, At: time.Now()}); err != nil {
		return err
	}
	j.point(PointReceiptSaved)
	return nil
}

// cleanupJournal is the journal cleanup writes, with the crash points around
// the deletes.
type cleanupJournal struct {
	store *journalfs.Store
	point func(string)
}

func (j cleanupJournal) BeginCleaning(id string) error {
	if err := j.store.BeginCleaning(id); err != nil {
		return err
	}
	j.point(PointCleaning)
	return nil
}

func (j cleanupJournal) FinishCleaning(id string) error {
	j.point(PointSourceDeleted)
	return j.store.FinishCleaning(id)
}

func (j cleanupJournal) FinishEmpty(id string) error { return j.store.FinishEmpty(id) }

// spoolIndex reports the highest segment index on the spool, which the
// writer may still be appending to.
type spoolIndex struct{ dir string }

func (s spoolIndex) HighestIndex(prefix string) (int, bool, error) {
	scan, err := segment.ScanDir(s.dir, []string{prefix})
	if err != nil {
		return 0, false, err
	}
	highest, found := 0, false
	for _, item := range scan.Active {
		if !found || item.Index > highest {
			highest, found = item.Index, true
		}
	}
	return highest, found, nil
}

// cleanLeftovers removes, and counts by kind, what another uploader left on
// the spool, using the patterns linked modules registered. Dynamo segments
// and this uploader's own directories are never touched.
func (u *Uploader) cleanLeftovers() {
	var segmentNames []*regexp.Regexp
	for _, prefix := range u.cfg.Prefixes() {
		segmentNames = append(segmentNames, regexp.MustCompile(`^`+regexp.QuoteMeta(prefix)+`\.[0-9]{6,}\.jsonl\.gz$`))
	}
	// Protect the top-level entry that holds each of this uploader's
	// directories, however deep they sit inside the spool.
	own := []string{}
	for _, dir := range []string{u.cfg.StateDir, u.cfg.QuarantineDir} {
		rel, err := filepath.Rel(u.cfg.SourceDir, dir)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		own = append(own, strings.SplitN(rel, string(filepath.Separator), 2)[0])
	}
	protected := func(name string) bool {
		if strings.HasPrefix(name, ".rtu-") || slices.Contains(own, name) {
			return true
		}
		return slices.ContainsFunc(segmentNames, func(pattern *regexp.Regexp) bool { return pattern.MatchString(name) })
	}
	report, err := foreign.DefaultConfig().Clean(u.cfg.SourceDir, protected)
	foreign.Record(u.metrics, report)
	if err != nil {
		slog.Warn("remove leftovers of another uploader", "error", safeError(err))
	}
	if len(report.Removed) > 0 || len(report.Kept) > 0 {
		slog.Info("found leftovers of another uploader", "removed", report.Removed, "kept", report.Kept)
	}
}
