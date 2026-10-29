// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/retry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

const (
	envCrashSpool   = "RTU_TEST_CRASH_SPOOL"
	envCrashLanding = "RTU_TEST_CRASH_LANDING"
)

// dirBackend lands each job in a directory, so jobs outlive the process
// that created them, as they do on a real destination. A job's files land
// before Submit returns, the way an upload commits before its response
// reaches the caller.
type dirBackend struct{ root string }

func (d dirBackend) Submit(_ context.Context, request backend.SubmitRequest) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	dir := filepath.Join(d.root, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	for i, file := range request.Transports() {
		if err := copyFile(file.Path, filepath.Join(dir, strings.Repeat("f", i+1)+".jsonl.gz")); err != nil {
			return "", err
		}
	}
	return id, os.WriteFile(filepath.Join(dir, "landed"), nil, 0o600)
}

func (d dirBackend) Status(_ context.Context, id string) (backend.Status, error) {
	if _, err := os.Stat(filepath.Join(d.root, id, "landed")); err == nil {
		return backend.StatusSuccess, nil
	}
	return backend.StatusGone, nil
}

func (d dirBackend) Capabilities() backend.Capabilities {
	return backend.Capabilities{
		AcceptedFormats: []backend.Format{backend.FormatGzipJSONL},
		Exports:         true,
		MaxBatchFiles:   16,
	}
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		return errors.Join(err, out.Close())
	}
	return out.Close()
}

// landedRequests counts, per request id, how often a record landed.
func landedRequests(t *testing.T, root string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	files, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		for _, line := range readGzipLines(t, file) {
			var record struct {
				Request struct {
					RequestID string `json:"request_id"`
				} `json:"request"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("landed line %q: %v", line, err)
			}
			counts[record.Request.RequestID]++
		}
	}
	return counts
}

// TestCrashChildRunsThePipeline is the child of the crash tests: it runs one
// pass and is killed at the point the parent named.
func TestCrashChildRunsThePipeline(t *testing.T) {
	if !uploadertest.CrashChild() {
		t.Skip("runs only as a crash child")
	}
	spool := &uploadertest.Spool{Dir: os.Getenv(envCrashSpool), Prefix: config.DefaultSegmentPrefix}
	uploader := newUploader(t, spool, dirBackend{root: os.Getenv(envCrashLanding)}, service.Options{Point: uploadertest.CrashPoint}, nil)
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
}

func TestACrashAtAnyPointLosesAndDoubleDeletesNothing(t *testing.T) {
	if uploadertest.CrashChild() {
		t.Skip("runs only in the parent")
	}
	points := []struct {
		name string
		// mayRepeat reports that a crash at this point can land the same
		// records twice: the job committed but its id was never saved, so
		// the restart cannot tell and submits again (accepted, counted).
		mayRepeat bool
	}{
		{name: service.PointPrepared},
		{name: service.PointBatchRecorded},
		{name: service.PointSubmitted, mayRepeat: true},
		{name: service.PointReceiptSaved},
		{name: service.PointConfirmed},
		{name: service.PointCleaning},
		{name: service.PointSourceDeleted},
	}
	for _, point := range points {
		t.Run(point.name, func(t *testing.T) {
			landing := t.TempDir()
			spool, closed := spoolWith(t,
				[]string{uploadertest.RequestEnd("a", 1), uploadertest.RequestEnd("b", 2)},
				[]string{uploadertest.RequestEnd("c", 3)},
			)
			result := uploadertest.RunCrash(t, "TestCrashChildRunsThePipeline", point.name,
				envCrashSpool+"="+spool.Dir, envCrashLanding+"="+landing)
			if !result.Killed || result.Point != point.name {
				t.Fatalf("child was not killed at %s:\n%s", point.name, result.Output)
			}

			// The writer moved on while the uploader was down: the old
			// active segment is closed now and must be uploaded too.
			oldActive := filepath.Join(spool.Dir, config.DefaultSegmentPrefix+".000002.jsonl.gz")
			newActive := spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("still-open", 9)).Bytes())

			restarted := newUploader(t, spool, dirBackend{root: landing}, service.Options{}, nil)
			for range 2 {
				if err := restarted.Pass(context.Background()); err != nil {
					t.Fatalf("Pass() after restart error = %v", err)
				}
			}
			if err := restarted.Close(); err != nil {
				t.Fatal(err)
			}

			landed := landedRequests(t, landing)
			for _, id := range []string{"a", "b", "c", "active"} {
				switch n := landed[id]; {
				case n == 0:
					t.Errorf("request %s was lost", id)
				case n > 1 && !point.mayRepeat:
					t.Errorf("request %s landed %d times", id, n)
				}
			}
			if landed["still-open"] != 0 {
				t.Error("the segment the writer still owns was uploaded")
			}
			for _, path := range append(closed, oldActive) {
				if !gone(path) {
					t.Errorf("source %s kept after its upload", filepath.Base(path))
				}
			}
			if gone(newActive) {
				t.Error("the segment the writer still owns was deleted")
			}
			states := journalStates(t, spool)
			for _, path := range append(closed, oldActive) {
				if state := states[filepath.Base(path)]; state != journal.StateDone {
					t.Errorf("journal state of %s = %q, want done", filepath.Base(path), state)
				}
			}
		})
	}
}

func retainFaults() *retry.Config {
	faults := fastFaults()
	faults.AmbiguousWithoutID = retry.AmbiguousRetain
	return faults
}

// TestCrashChildRunsThePipelineRetaining is the crash child for an uploader
// configured to retain ambiguous submits.
func TestCrashChildRunsThePipelineRetaining(t *testing.T) {
	if !uploadertest.CrashChild() {
		t.Skip("runs only as a crash child")
	}
	spool := &uploadertest.Spool{Dir: os.Getenv(envCrashSpool), Prefix: config.DefaultSegmentPrefix}
	uploader := newUploader(t, spool, dirBackend{root: os.Getenv(envCrashLanding)}, service.Options{Point: uploadertest.CrashPoint, Faults: retainFaults()}, nil)
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
}

func TestACrashAfterSubmitIsNotResubmittedWhenAmbiguousSubmitsAreRetained(t *testing.T) {
	if uploadertest.CrashChild() {
		t.Skip("runs only in the parent")
	}
	landing := t.TempDir()
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	result := uploadertest.RunCrash(t, "TestCrashChildRunsThePipelineRetaining", service.PointSubmitted,
		envCrashSpool+"="+spool.Dir, envCrashLanding+"="+landing)
	if !result.Killed || result.Point != service.PointSubmitted {
		t.Fatalf("child was not killed at %s:\n%s", service.PointSubmitted, result.Output)
	}

	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	restarted := newUploader(t, spool, dirBackend{root: landing}, service.Options{Faults: retainFaults(), Metrics: metrics}, nil)
	for range 3 {
		_ = restarted.Pass(context.Background())
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	if n := landedRequests(t, landing)["a"]; n != 1 {
		t.Fatalf("request a landed %d times, want the retained batch not submitted again", n)
	}
	if gone(closed[0]) {
		t.Fatal("source deleted, want it kept for an operator")
	}
	if got, _ := metrics.Value(telemetry.SubmitsAmbiguous, nil); got != 1 {
		t.Errorf("submits_ambiguous = %v, want the retained batch counted once", got)
	}
	for _, b := range journalBatches(t, spool) {
		if b.NextRetryAt.Before(time.Now().Add(365 * 24 * time.Hour)) {
			t.Fatalf("retained batch state %s retries at %v, want it parked for an operator", b.State, b.NextRetryAt)
		}
	}
}
