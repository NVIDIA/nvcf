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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

// TestWaitAndLogProcessExit covers waitAndLogProcessExit directly: both runSecretsCheckLoop
// call sites (interrupt shutdown and secret-triggered restart) delegate to this same helper, so
// exercising it once here covers both.
func TestWaitAndLogProcessExit(t *testing.T) {
	tests := []struct {
		name    string
		waitErr error
		wantLog bool
	}{
		{name: "wait error is logged with its message", waitErr: errors.New("wait: no child processes"), wantLog: true},
		{name: "a different wait error message is preserved verbatim", waitErr: errors.New("signal: killed"), wantLog: true},
		{name: "clean exit logs nothing", waitErr: nil, wantLog: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := logger.Logger
			t.Cleanup(func() { logger.Logger = orig })

			core, logs := observer.New(zapcore.DebugLevel)
			logger.Logger = zap.New(core).Sugar()

			waitAndLogProcessExit(&fakeProcess{waitErr: tt.waitErr})

			entries := logs.TakeAll()
			if !tt.wantLog {
				if len(entries) != 0 {
					t.Fatalf("expected no log entries, got %d", len(entries))
				}
				return
			}
			if len(entries) != 1 {
				t.Fatalf("expected 1 log entry, got %d", len(entries))
			}
			if !strings.Contains(entries[0].Message, tt.waitErr.Error()) {
				t.Fatalf("expected log message to contain %q, got %q", tt.waitErr.Error(), entries[0].Message)
			}
		})
	}
}

func TestOtelCollectorArgs(t *testing.T) {
	const gate = "ottl.functions.enableLambda"

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "adds config and feature gate when absent",
			args: nil,
			want: []string{"--config", "/tmp/cfg.yaml", "--feature-gates", gate},
		},
		{
			name: "keeps a caller-supplied config path",
			args: []string{"--config", "/custom.yaml"},
			want: []string{"--config", "/custom.yaml", "--feature-gates", gate},
		},
		{
			name: "recognizes the --config=value form and does not duplicate it",
			args: []string{"--config=/custom.yaml"},
			want: []string{"--config=/custom.yaml", "--feature-gates", gate},
		},
		{
			// The required gate must survive alongside caller-supplied gates:
			// without it the rendered config does not parse and the collector
			// never starts. Repeated --feature-gates flags accumulate.
			name: "appends the required gate alongside caller-supplied gates",
			args: []string{"--feature-gates", "ottl.PanicDuplicateName"},
			want: []string{"--feature-gates", "ottl.PanicDuplicateName", "--config", "/tmp/cfg.yaml", "--feature-gates", gate},
		},
		{
			name: "appends the required gate alongside the --feature-gates=value form",
			args: []string{"--feature-gates=ottl.PanicDuplicateName"},
			want: []string{"--feature-gates=ottl.PanicDuplicateName", "--config", "/tmp/cfg.yaml", "--feature-gates", gate},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := otelCollectorArgs(tt.args, "/tmp/cfg.yaml")
			assert.Equal(t, tt.want, got)
		})
	}
}

// The rendered metrics pipeline uses an OTTL lambda, which the collector
// rejects at config-parse time unless this gate is on. If the gate name ever
// changes upstream, this fails alongside the CI runtime startup check.
func TestOtelCollectorFeatureGateIsLambda(t *testing.T) {
	assert.Equal(t, "ottl.functions.enableLambda", otelCollectorFeatureGates)
}
