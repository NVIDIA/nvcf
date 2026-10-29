// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// A missing secrets file stops startup before any segment is read or sent.
func TestStartupStopsWithoutTheSecretsFile(t *testing.T) {
	spool, closed := spoolWith(t, []string{uploadertest.RequestEnd("a", 1)})
	fake := backendtest.NewFake()
	missing := filepath.Join(t.TempDir(), "missing.json")
	uploader := newUploader(t, spool, fake, service.Options{}, map[string]string{config.EnvSecretsFile: missing})

	if err := uploader.Pass(context.Background()); err == nil {
		t.Fatal("Pass() error = nil, want startup to fail without the secrets file")
	}
	if fake.SubmitCalls() != 0 || gone(closed[0]) {
		t.Fatalf("submit calls = %d, want the segment left in place and nothing sent", fake.SubmitCalls())
	}
}
