// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package batch_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/batch"
)

const gib = int64(1) << 30

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type observations struct {
	mu     sync.Mutex
	values map[string][]float64
}

func (o *observations) Observe(name string, value float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.values == nil {
		o.values = map[string][]float64{}
	}
	o.values[name] = append(o.values[name], value)
}

func newBatcher(t *testing.T, cfg batch.Config, limits batch.Limits, metrics batch.Metrics) *batch.Batcher {
	t.Helper()
	b, err := batch.New(cfg, limits, batch.Options{Metrics: metrics})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b
}

var batchingOn = batch.Limits{Batching: true}

func member(n int, dest string, bytes int64, preparedAt time.Time) batch.Member {
	return batch.Member{
		SegmentID:   fmt.Sprintf("seg-%02d", n),
		Destination: dest,
		File:        batch.File{Path: fmt.Sprintf("/state/transport/seg-%02d.%s.parquet", n, dest), SHA256: "ab", Bytes: bytes, Rows: 10},
		PreparedAt:  preparedAt,
	}
}

func add(t *testing.T, b *batch.Batcher, m batch.Member, now time.Time) []batch.Batch {
	t.Helper()
	closed, err := b.Add(m, now)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return closed
}

func ids(b batch.Batch) []string {
	out := make([]string, 0, len(b.Members))
	for _, m := range b.Members {
		out = append(out, m.SegmentID)
	}
	return out
}

func TestBatchClosesOnTheFirstTrigger(t *testing.T) {
	cases := []struct {
		name       string
		cfg        batch.Config
		sizes      []int64
		wantClosed int
		wantReason batch.CloseReason
		wantSize   int
	}{
		{"segments", batch.Config{MaxSegments: 3, MaxBytes: gib, MaxFiles: 500, MaxWait: time.Minute}, []int64{10, 10, 10}, 2, batch.ReasonSegments, 3},
		{"files", batch.Config{MaxSegments: 100, MaxBytes: gib, MaxFiles: 2, MaxWait: time.Minute}, []int64{10, 10}, 1, batch.ReasonFiles, 2},
		{"bytes reached exactly", batch.Config{MaxSegments: 16, MaxBytes: 100, MaxFiles: 500, MaxWait: time.Minute}, []int64{60, 40}, 1, batch.ReasonBytes, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBatcher(t, tc.cfg, batchingOn, nil)
			var closed []batch.Batch
			for i, size := range tc.sizes {
				got := add(t, b, member(i+1, "primary", size, t0), t0)
				if i < len(tc.sizes)-1 && len(got) != 0 {
					t.Fatalf("batch closed early after member %d: %+v", i+1, got)
				}
				closed = append(closed, got...)
			}
			if len(closed) != 1 || closed[0].Reason != tc.wantReason || len(closed[0].Members) != tc.wantSize {
				t.Fatalf("closed = %+v, want one batch of %d closed by %s", closed, tc.wantSize, tc.wantReason)
			}
			if b.Pending() != 0 {
				t.Fatalf("pending = %d after close, want 0", b.Pending())
			}
		})
	}
}

func TestMemberThatWouldOverflowBytesClosesTheBatchFirst(t *testing.T) {
	b := newBatcher(t, batch.Config{MaxSegments: 16, MaxBytes: 100, MaxFiles: 500, MaxWait: time.Minute}, batchingOn, nil)
	add(t, b, member(1, "primary", 60, t0), t0)
	closed := add(t, b, member(2, "primary", 50, t0), t0)

	if len(closed) != 1 || closed[0].Reason != batch.ReasonBytes || closed[0].Bytes != 60 || len(closed[0].Members) != 1 {
		t.Fatalf("closed = %+v, want the 60-byte batch closed before the overflow", closed)
	}
	if b.Pending() != 1 {
		t.Fatalf("pending = %d, want the 50-byte member waiting in a new batch", b.Pending())
	}
}

func TestLoneMemberClosesAfterMaxWait(t *testing.T) {
	b := newBatcher(t, batch.DefaultConfig(), batchingOn, nil)
	add(t, b, member(1, "primary", 10, t0), t0)

	if due, ok := b.NextDue(); !ok || !due.Equal(t0.Add(time.Minute)) {
		t.Fatalf("NextDue = %s, %v; want %s", due, ok, t0.Add(time.Minute))
	}
	if closed := b.Due(t0.Add(59 * time.Second)); len(closed) != 0 {
		t.Fatalf("closed before the wait: %+v", closed)
	}
	closed := b.Due(t0.Add(time.Minute))
	if len(closed) != 1 || closed[0].Reason != batch.ReasonMaxWait || len(closed[0].Members) != 1 {
		t.Fatalf("closed = %+v, want the lone member closed by the wait", closed)
	}
	if _, ok := b.NextDue(); ok {
		t.Fatal("NextDue reports a lane after every lane closed")
	}
}

func TestMaxWaitCountsFromTheOldestMember(t *testing.T) {
	b := newBatcher(t, batch.DefaultConfig(), batchingOn, nil)
	add(t, b, member(1, "primary", 10, t0), t0)
	add(t, b, member(2, "primary", 10, t0.Add(30*time.Second)), t0.Add(30*time.Second))

	closed := b.Due(t0.Add(time.Minute))
	if len(closed) != 1 || len(closed[0].Members) != 2 {
		t.Fatalf("closed = %+v, want both members once the oldest waited a minute", closed)
	}
}

func TestMemberAddedToAnOverdueLaneClosesIt(t *testing.T) {
	b := newBatcher(t, batch.DefaultConfig(), batchingOn, nil)
	add(t, b, member(1, "primary", 10, t0), t0)

	closed := add(t, b, member(2, "primary", 10, t0.Add(61*time.Second)), t0.Add(61*time.Second))
	if len(closed) != 1 || closed[0].Reason != batch.ReasonMaxWait || len(closed[0].Members) != 2 {
		t.Fatalf("closed = %+v, want the overdue lane closed with the new member", closed)
	}
}

func TestLanesNeverMixDestinations(t *testing.T) {
	b := newBatcher(t, batch.Config{MaxSegments: 2, MaxBytes: gib, MaxFiles: 500, MaxWait: time.Minute}, batchingOn, nil)
	var closed []batch.Batch
	for i, dest := range []string{"primary", "legacy", "primary", "legacy"} {
		closed = append(closed, add(t, b, member(i+1, dest, 10, t0), t0)...)
	}
	if len(closed) != 2 {
		t.Fatalf("closed %d batches, want one per destination", len(closed))
	}
	for _, c := range closed {
		for _, m := range c.Members {
			if m.Destination != c.Destination {
				t.Fatalf("batch for %s holds a member for %s", c.Destination, m.Destination)
			}
		}
	}
	if got := ids(closed[0]); strings.Join(got, ",") != "seg-01,seg-03" {
		t.Fatalf("first batch members = %v, want seg-01,seg-03 in arrival order", got)
	}
}

func TestBatchingOffGivesBatchesOfOne(t *testing.T) {
	b := newBatcher(t, batch.DefaultConfig(), batch.Limits{Batching: false}, nil)
	closed := add(t, b, member(1, "bucket/prefix", 10, t0), t0)
	if len(closed) != 1 || closed[0].Reason != batch.ReasonSolo || len(closed[0].Members) != 1 {
		t.Fatalf("closed = %+v, want a batch of one", closed)
	}
	if b.Pending() != 0 {
		t.Fatalf("pending = %d, want 0 with batching off", b.Pending())
	}
}

func TestResolveLowersSettingsAboveTheBackendLimits(t *testing.T) {
	cfg := batch.Config{MaxSegments: 16, MaxBytes: 3 * gib, MaxFiles: 600, MaxWait: time.Minute}
	got, warnings := batch.Resolve(cfg, batch.Limits{Batching: true, MaxBytes: 2 * gib, MaxFiles: 500})
	if got.MaxBytes != 2*gib || got.MaxFiles != 500 {
		t.Fatalf("resolved = %+v, want bytes clamped to 2 GiB and files to 500", got)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], "bytes") || !strings.Contains(warnings[1], "files") {
		t.Fatalf("warnings = %q, want one for bytes and one for files", warnings)
	}

	got, warnings = batch.Resolve(batch.DefaultConfig(), batch.Limits{Batching: true, MaxBytes: 2 * gib, MaxFiles: 500})
	if got != batch.DefaultConfig() || len(warnings) != 0 {
		t.Fatalf("defaults resolved to %+v with %q, want unchanged and no warnings", got, warnings)
	}
}

func TestAddRejectsMembersThatCannotBeBatched(t *testing.T) {
	b := newBatcher(t, batch.Config{MaxSegments: 16, MaxBytes: 100, MaxFiles: 500, MaxWait: time.Minute}, batchingOn, nil)
	add(t, b, member(1, "primary", 10, t0), t0)

	if _, err := b.Add(member(2, "primary", 101, t0), t0); !errors.Is(err, batch.ErrMemberTooLarge) {
		t.Fatalf("oversized member error = %v, want ErrMemberTooLarge", err)
	}
	cases := []struct {
		name   string
		member batch.Member
	}{
		{"duplicate", member(1, "primary", 10, t0)},
		{"no segment id", batch.Member{Destination: "primary", File: batch.File{Path: "/x", Bytes: 1}}},
		{"no destination", batch.Member{SegmentID: "s", File: batch.File{Path: "/x", Bytes: 1}}},
		{"no path", batch.Member{SegmentID: "s", Destination: "primary", File: batch.File{Bytes: 1}}},
		{"negative bytes", batch.Member{SegmentID: "s", Destination: "primary", File: batch.File{Path: "/x", Bytes: -1}, PreparedAt: t0}},
		{"no prepared time", batch.Member{SegmentID: "s", Destination: "primary", File: batch.File{Path: "/x", Bytes: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := b.Add(tc.member, t0); err == nil {
				t.Fatalf("Add accepted %s", tc.name)
			}
		})
	}
	if b.Pending() != 1 {
		t.Fatalf("pending = %d, want only the first member", b.Pending())
	}
}

func TestSoloClosesABatchOfOneWithoutTouchingLanes(t *testing.T) {
	b := newBatcher(t, batch.DefaultConfig(), batchingOn, nil)
	add(t, b, member(1, "primary", 10, t0), t0)

	solo, err := b.Solo(member(2, "primary", 10, t0), t0)
	if err != nil || solo.Reason != batch.ReasonSolo || len(solo.Members) != 1 || solo.Members[0].SegmentID != "seg-02" {
		t.Fatalf("Solo = %+v, %v; want a batch of seg-02 alone", solo, err)
	}
	if b.Pending() != 1 {
		t.Fatalf("pending = %d, want the open lane untouched", b.Pending())
	}
}

func TestClosedBatchesHaveUniqueSafeIDsAndAreObserved(t *testing.T) {
	metrics := &observations{}
	b := newBatcher(t, batch.Config{MaxSegments: 2, MaxBytes: gib, MaxFiles: 500, MaxWait: time.Minute}, batchingOn, metrics)
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	for i := 1; i <= 6; i++ {
		for _, c := range add(t, b, member(i, "primary", 25, t0), t0) {
			if !pattern.MatchString(c.ID) || seen[c.ID] {
				t.Fatalf("batch id %q is unsafe or repeated", c.ID)
			}
			seen[c.ID] = true
			if c.Destination != "primary" || c.Bytes != 50 || !c.ClosedAt.Equal(t0) {
				t.Fatalf("batch = %+v, want primary, 50 bytes, closed at t0", c)
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("closed %d batches, want 3", len(seen))
	}
	if got := metrics.values[batch.MetricBatchSegments]; len(got) != 3 || got[0] != 2 {
		t.Fatalf("%s = %v, want three observations of 2", batch.MetricBatchSegments, got)
	}
	if got := metrics.values[batch.MetricBatchBytes]; len(got) != 3 || got[0] != 50 {
		t.Fatalf("%s = %v, want three observations of 50", batch.MetricBatchBytes, got)
	}
}

func TestConfigValidation(t *testing.T) {
	if err := batch.DefaultConfig().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*batch.Config)
	}{
		{"zero segments", func(c *batch.Config) { c.MaxSegments = 0 }},
		{"zero bytes", func(c *batch.Config) { c.MaxBytes = 0 }},
		{"zero files", func(c *batch.Config) { c.MaxFiles = 0 }},
		{"zero wait", func(c *batch.Config) { c.MaxWait = 0 }},
		{"negative wait", func(c *batch.Config) { c.MaxWait = -time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := batch.DefaultConfig()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if _, err := batch.New(cfg, batchingOn, batch.Options{}); err == nil {
				t.Fatalf("New accepted %s", tc.name)
			}
		})
	}
}

// fakeJournal records calls in order and can fail either step.
type fakeJournal struct {
	calls    []string
	openErr  error
	saveErr  error
	receipts map[string]string
}

func (j *fakeJournal) OpenBatch(b batch.Batch, backend string) error {
	j.calls = append(j.calls, "open "+b.ID+" "+backend)
	return j.openErr
}

func (j *fakeJournal) SaveReceipt(batchID, backend, receiptID string) error {
	j.calls = append(j.calls, "save "+batchID+" "+backend)
	if j.saveErr != nil {
		return j.saveErr
	}
	if j.receipts == nil {
		j.receipts = map[string]string{}
	}
	j.receipts[batchID] = receiptID
	return nil
}

func submitter(j *fakeJournal, receipt string, err error) batch.SubmitFunc {
	return func(_ context.Context, b batch.Batch) (string, error) {
		j.calls = append(j.calls, fmt.Sprintf("submit %s with %d files", b.ID, len(b.Members)))
		return receipt, err
	}
}

func closedBatch() batch.Batch {
	return batch.Batch{ID: "b1", Destination: "primary", Members: []batch.Member{member(1, "primary", 10, t0), member(2, "primary", 10, t0)}}
}

func TestSubmitRecordsTheBatchBeforeCallingTheBackend(t *testing.T) {
	j := &fakeJournal{}
	receipt, err := batch.Submit(t.Context(), j, "fake", closedBatch(), submitter(j, "job-1", nil))
	if err != nil || receipt != "job-1" {
		t.Fatalf("Submit = %q, %v; want job-1", receipt, err)
	}
	want := []string{"open b1 fake", "submit b1 with 2 files", "save b1 fake"}
	if strings.Join(j.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", j.calls, want)
	}
	if j.receipts["b1"] != "job-1" {
		t.Fatalf("saved receipt = %q, want job-1", j.receipts["b1"])
	}
}

func TestSubmitDoesNotCallTheBackendWhenTheRecordFails(t *testing.T) {
	j := &fakeJournal{openErr: errors.New("disk full")}
	if _, err := batch.Submit(t.Context(), j, "fake", closedBatch(), submitter(j, "job-1", nil)); err == nil {
		t.Fatal("Submit succeeded although the batch was not recorded")
	}
	if len(j.calls) != 1 {
		t.Fatalf("calls = %q, want only the failed record", j.calls)
	}
}

func TestSendSavesAReceiptThatCameBackWithAnError(t *testing.T) {
	submitErr := errors.New("marker upload failed")
	j := &fakeJournal{}
	receipt, err := batch.Send(t.Context(), j, "fake", closedBatch(), submitter(j, "job-1", submitErr))
	if !errors.Is(err, submitErr) || receipt != "job-1" || j.receipts["b1"] != "job-1" {
		t.Fatalf("Send = %q, %v (saved %q); want job-1 saved and the submit error returned", receipt, err, j.receipts["b1"])
	}

	j = &fakeJournal{}
	receipt, err = batch.Send(t.Context(), j, "fake", closedBatch(), submitter(j, "", submitErr))
	if !errors.Is(err, submitErr) || receipt != "" || len(j.receipts) != 0 {
		t.Fatalf("Send with no receipt = %q, %v (saved %v); want nothing saved", receipt, err, j.receipts)
	}
}

func TestSendReportsAReceiptItCouldNotSave(t *testing.T) {
	submitErr := errors.New("timeout")
	j := &fakeJournal{saveErr: errors.New("disk full")}
	receipt, err := batch.Send(t.Context(), j, "fake", closedBatch(), submitter(j, "job-1", submitErr))
	if !errors.Is(err, batch.ErrReceiptNotSaved) || !errors.Is(err, submitErr) || receipt != "job-1" {
		t.Fatalf("Send = %q, %v; want ErrReceiptNotSaved and the submit error, with the receipt", receipt, err)
	}
}

func TestSendTreatsASuccessWithoutAReceiptAsAmbiguous(t *testing.T) {
	j := &fakeJournal{}
	receipt, err := batch.Send(t.Context(), j, "fake", closedBatch(), submitter(j, "", nil))
	if receipt != "" || !errors.Is(err, batch.ErrEmptyReceipt) {
		t.Fatalf("Send = %q, %v; want ErrEmptyReceipt", receipt, err)
	}
	if fault := backend.ClassifyDefault(err); fault.Class != backend.ClassAmbiguous {
		t.Fatalf("Send error class = %s, want ambiguous so the batch is not treated as sent", fault.Class)
	}
	if len(j.receipts) != 0 {
		t.Fatalf("saved receipts = %v, want none", j.receipts)
	}
}
