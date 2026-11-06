// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// unconfirmedJob is a job that fails in a way that may still have landed,
// as a job whose upload the destination could not confirm.
func unconfirmedJob() backendtest.SubmitStep {
	return backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{
		Status: backend.StatusFailed,
		Err:    &backend.Fault{Class: backend.ClassUncertain, Scope: backend.ScopeBackend, Reason: backend.ReasonUploadUnconfirmed},
	}}}
}

func batchesIn(t *testing.T, spool *uploadertest.Spool, state journal.State) int {
	t.Helper()
	n := 0
	for _, b := range journalBatches(t, spool) {
		if b.State == state {
			n++
		}
	}
	return n
}

func TestAJobWhoseUploadIsUnconfirmedIsSentAgainOnceThenStranded(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(unconfirmedJob(), unconfirmedJob(), unconfirmedJob())
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics}, nil)

	passUntil(t, uploader, func() bool { stranded, _ := metrics.Value(telemetry.JobsStranded, nil); return stranded > 0 })
	for range 5 {
		_ = uploader.Pass(context.Background())
	}
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}

	if got := fake.SubmitCalls(); got != 2 {
		t.Fatalf("submits = %d, want the job and one resend", got)
	}
	if got, _ := metrics.Value(telemetry.SubmitsAmbiguous, nil); got != 1 {
		t.Fatalf("submits_ambiguous = %v, want the resend counted as a possible duplicate", got)
	}
	if got := batchesIn(t, spool, journal.StateStranded); got != 1 {
		t.Fatalf("stranded batches = %d, want the resent job kept for an operator", got)
	}
	if gone(closed[0]) {
		t.Fatal("the source was deleted although no job confirmed it")
	}
}

func TestTheOneResendOfAnUnconfirmedUploadSurvivesARestart(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(unconfirmedJob(), unconfirmedJob(), unconfirmedJob())

	first := newUploader(t, spool, fake, service.Options{}, nil)
	_ = first.Pass(context.Background())
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got := batchesIn(t, spool, journal.StateReleased); got != 1 {
		t.Fatalf("released batches = %d after the first failure, want the job released for its one resend", got)
	}

	second := newUploader(t, spool, fake, service.Options{}, nil)
	passUntil(t, second, func() bool { return fake.SubmitCalls() >= 2 })
	for range 5 {
		_ = second.Pass(context.Background())
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fake.SubmitCalls(); got != 2 {
		t.Fatalf("submits = %d across a restart, want the job and one resend", got)
	}
	if got := batchesIn(t, spool, journal.StateStranded); got != 1 {
		t.Fatalf("stranded batches = %d, want the resent job kept for an operator", got)
	}
	if gone(closed[0]) {
		t.Fatal("the source was deleted although no job confirmed it")
	}
}

func TestStatusReadsThatKeepFailingPastTheMaxJobAgeStrandTheJob(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{
		Err: errors.New("status endpoint unavailable"),
	}}})
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, fake, service.Options{Metrics: metrics, Now: clock}, nil)

	_ = uploader.Pass(context.Background())
	if got, _ := metrics.Value(telemetry.JobsStranded, nil); got != 0 {
		t.Fatalf("jobs_stranded = %v before the max job age, want 0", got)
	}
	offset.Store(int64(25 * time.Hour))
	_ = uploader.Pass(context.Background())
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}

	if got, _ := metrics.Value(telemetry.JobsStranded, nil); got != 1 {
		t.Fatalf("jobs_stranded = %v, want the unreadable job stranded past the max job age", got)
	}
	if got := batchesIn(t, spool, journal.StateStranded); got != 1 {
		t.Fatalf("stranded batches = %d, want 1", got)
	}
	if got := fake.SubmitCalls(); got != 1 {
		t.Fatalf("submits = %d, want a job with an id never sent again", got)
	}
	if gone(closed[0]) {
		t.Fatal("the source was deleted although no job confirmed it")
	}
}

func TestAJobTheBackendHasNoRecordOfIsStranded(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Statuses: []backendtest.StatusStep{{Status: backend.StatusNotFound}}})
	uploader := newUploader(t, spool, fake, service.Options{}, nil)

	_ = uploader.Pass(context.Background())
	if err := uploader.Close(); err != nil {
		t.Fatal(err)
	}
	if got := batchesIn(t, spool, journal.StateStranded); got != 1 {
		t.Fatalf("stranded batches = %d, want the job kept for an operator", got)
	}
	if gone(closed[0]) {
		t.Fatal("the source was deleted although no job confirmed it")
	}
}
