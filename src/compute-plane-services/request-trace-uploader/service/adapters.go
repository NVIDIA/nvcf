// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
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
	// exports records on each receipt whether the submit was a real export.
	exports bool
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
	if err := j.store.SetReceipt(batchID, journal.Receipt{Backend: backendName, ID: receiptID, At: time.Now(), Exported: j.exports}); err != nil {
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
