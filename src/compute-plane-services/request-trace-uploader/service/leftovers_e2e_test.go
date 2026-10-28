// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// Loss during a rollout is accepted, so files another uploader left on a
// reused spool stay where they are: startup removes nothing it did not write.
func TestFilesAnotherUploaderLeftAreLeftAlone(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	sidecar := closed[0] + ".old-uploader-id"
	workDir := filepath.Join(spool.Dir, ".old-uploader-work")
	if err := os.WriteFile(sidecar, []byte("job"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		t.Fatal(err)
	}
	uploader := newUploader(t, spool, backendtest.NewFake(), service.Options{}, nil)

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	if !gone(closed[0]) {
		t.Fatal("the closed segment was not exported")
	}
	for _, path := range []string{sidecar, workDir} {
		if gone(path) {
			t.Errorf("%s was removed, want files another uploader left kept", filepath.Base(path))
		}
	}
}
