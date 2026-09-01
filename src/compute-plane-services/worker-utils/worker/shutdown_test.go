/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// sigtermRunError is the exact error the nvkit servers run group returns when
// the process receives SIGTERM, which is how Kubernetes always asks a container
// to stop. Reproduced verbatim from a production utils crash.
const sigtermRunError = "received signal terminated"

// sigintRunError is the SIGINT equivalent.
const sigintRunError = "received signal interrupt"

func TestIsShutdownSignalError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "sigterm", err: errors.New(sigtermRunError), want: true},
		{name: "sigint", err: errors.New(sigintRunError), want: true},
		{
			name: "wrapped sigterm",
			err:  fmt.Errorf("internal error: %w", errors.New(sigtermRunError)),
			want: true,
		},
		{name: "unrelated failure", err: errors.New("listen tcp :9191: address already in use"), want: false},
		{name: "signal-shaped but not a shutdown signal", err: errors.New("received signal killed"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsShutdownSignalError(tt.err); got != tt.want {
				t.Fatalf("IsShutdownSignalError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsFatalServerError_SIGTERMBeforeShutdownCancel is the regression test for
// the utils panic at worker.go "internal error: received signal terminated".
// The server run group always returns a non-nil error on SIGTERM, so when the
// shutdown context has not been cancelled yet the old guard classified a normal
// pod termination as a crash.
func TestIsFatalServerError_SIGTERMBeforeShutdownCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w := &NVCFWorker{shutdownCtx: ctx}

	if w.isFatalServerError(errors.New(sigtermRunError)) {
		t.Fatal("SIGTERM with a live shutdown context must not be treated as a fatal server error")
	}
}

func TestIsFatalServerError(t *testing.T) {
	tests := []struct {
		name             string
		err              error
		cancelBeforeCall bool
		want             bool
	}{
		{name: "no error", err: nil, want: false},
		{name: "sigterm", err: errors.New(sigtermRunError), want: false},
		{name: "sigint", err: errors.New(sigintRunError), want: false},
		{name: "real failure during shutdown", err: errors.New("boom"), cancelBeforeCall: true, want: false},
		{name: "real failure while running", err: errors.New("boom"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			if tt.cancelBeforeCall {
				cancel()
			}

			w := &NVCFWorker{shutdownCtx: ctx}

			if got := w.isFatalServerError(tt.err); got != tt.want {
				t.Fatalf("isFatalServerError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
