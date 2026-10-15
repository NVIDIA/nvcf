// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteHeartbeatThenCheckLivenessFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(heartbeatFileEnvVar, path)

	if err := writeHeartbeat(path); err != nil {
		t.Fatalf("writeHeartbeat: %v", err)
	}
	if err := CheckLiveness(); err != nil {
		t.Errorf("CheckLiveness on a fresh heartbeat: %v", err)
	}
}

func TestWriteHeartbeatIsAtomicOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")

	if err := writeHeartbeat(path); err != nil {
		t.Fatalf("first writeHeartbeat: %v", err)
	}
	if err := writeHeartbeat(path); err != nil {
		t.Fatalf("second writeHeartbeat: %v", err)
	}

	// The rename leaves exactly one file (the heartbeat) and no temp leftovers.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir contents = %v, want only %q", names, filepath.Base(path))
	}
}

func TestCheckLivenessMissingFile(t *testing.T) {
	t.Setenv(heartbeatFileEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))

	if err := CheckLiveness(); err == nil {
		t.Error("CheckLiveness on a missing heartbeat returned nil, want error")
	}
}

func TestCheckLivenessUnparseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(heartbeatFileEnvVar, path)
	if err := os.WriteFile(path, []byte("not-a-timestamp"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := CheckLiveness(); err == nil {
		t.Error("CheckLiveness on an unparseable heartbeat returned nil, want error")
	}
}

func TestCheckLivenessStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(heartbeatFileEnvVar, path)

	// A beat older than livenessMaxAge must read as stale.
	stale := time.Now().UTC().Add(-livenessMaxAge() - time.Minute).Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := CheckLiveness(); err == nil {
		t.Error("CheckLiveness on a stale heartbeat returned nil, want error")
	}
}

func TestRunHealthChecksWritesHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heartbeat")
	t.Setenv(heartbeatFileEnvVar, path)

	// The monitor beats once on entry, before any tick fires. Keep the interval
	// long so the test observes that initial beat rather than a cycle beat.
	orig := healthCheckInterval
	healthCheckInterval = time.Hour
	t.Cleanup(func() { healthCheckInterval = orig })

	m := &Monitor{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.RunHealthChecks(ctx)
		close(done)
	}()

	var ok bool
	for range 200 {
		if _, err := os.Stat(path); err == nil {
			ok = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if !ok {
		t.Error("RunHealthChecks did not write the heartbeat file on entry")
	}
}
