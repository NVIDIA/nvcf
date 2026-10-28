// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/foreign"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// Patterns a module that replaces another uploader would register. The
// greedy one matches every segment name, so the test also shows that
// segments are protected whatever a pattern says.
func init() {
	foreign.Register(foreign.Pattern{Kind: "old_job_id", Glob: "*.old-uploader-id", Action: foreign.Remove})
	foreign.Register(foreign.Pattern{Kind: "old_quarantine", Glob: "old-uploader-failed", Action: foreign.Keep})
	foreign.Register(foreign.Pattern{Kind: "greedy", Glob: "*.jsonl.gz", Action: foreign.Remove})
}

func TestLeftoversOfAnotherUploaderAreRemovedAndCountedAtStartup(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	jobID := closed[0] + ".old-uploader-id"
	failed := filepath.Join(spool.Dir, "old-uploader-failed")
	if err := os.WriteFile(jobID, []byte("job"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(failed, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x.jsonl.gz", "y.jsonl.gz"} {
		if err := os.WriteFile(filepath.Join(failed, name), []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	exporter := newCapture(t, backendtest.NewFake())
	uploader := newUploader(t, spool, exporter, service.Options{Metrics: metrics}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	if !gone(jobID) {
		t.Error("the other uploader's job id file was kept")
	}
	if _, err := os.Stat(filepath.Join(failed, "x.jsonl.gz")); err != nil {
		t.Errorf("the other uploader's quarantine was not kept: %v", err)
	}
	if got := len(exporter.exported()); got != 1 {
		t.Fatalf("exported %d records, want the closed segment uploaded as usual", got)
	}
	if active := filepath.Join(spool.Dir, "request-trace.000001.jsonl.gz"); gone(active) {
		t.Error("a pattern removed the segment the writer still owns")
	}
	for _, want := range []struct {
		name  string
		kind  string
		value float64
	}{
		{telemetry.LeftoversRemoved, "old_job_id", 1},
		{telemetry.LeftoversKept, "old_quarantine", 2},
		{telemetry.LeftoversRemoved, "greedy", 0},
	} {
		if got, _ := metrics.Value(want.name, telemetry.Labels{"kind": want.kind}); got != want.value {
			t.Errorf("%s{kind=%s} = %v, want %v", want.name, want.kind, got, want.value)
		}
	}
}
