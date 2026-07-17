/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"errors"
	"os"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
)

type fakeProcess struct {
	waitErr error
}

func (f *fakeProcess) Wait() (*os.ProcessState, error) {
	return nil, f.waitErr
}

func TestWaitAndLogProcessExit(t *testing.T) {
	tests := []struct {
		name    string
		waitErr error
		wantLog bool
	}{
		{name: "interrupt shutdown wait error is logged", waitErr: errors.New("wait: no child processes"), wantLog: true},
		{name: "secret-triggered restart wait error is logged", waitErr: errors.New("signal: killed"), wantLog: true},
		{name: "clean exit logs nothing", waitErr: nil, wantLog: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.DebugLevel)
			logger.Logger = zap.New(core).Sugar()

			waitAndLogProcessExit(&fakeProcess{waitErr: tt.waitErr})

			entries := logs.TakeAll()
			if tt.wantLog && len(entries) != 1 {
				t.Fatalf("expected 1 log entry, got %d", len(entries))
			}
			if !tt.wantLog && len(entries) != 0 {
				t.Fatalf("expected no log entries, got %d", len(entries))
			}
		})
	}
}
