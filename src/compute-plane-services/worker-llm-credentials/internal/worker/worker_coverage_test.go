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
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-llm-credentials/configs"
)

// installLogObserver swaps in an in-memory zap core for the duration of a test
// and returns the observed-logs sink. The persist callback in Run logs
// "writing worker token to disk" at the very start of every invocation, before
// any error branch, so the presence of that entry is a reliable observable that
// the callback executed. The original global logger is restored on cleanup.
func installLogObserver(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	t.Cleanup(restore)
	return logs
}

// waitForCallbackInvoked polls the observed logs until the persist callback has
// logged its start line, or the deadline elapses. Returns true if observed.
func waitForCallbackInvoked(logs *observer.ObservedLogs) bool {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// This must match the log message in worker.go's persist callback
		// verbatim; if that message is reworded, update it here too.
		if len(logs.FilterMessage("writing worker token to disk").All()) > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

// TestNew_DefaultsWorkerTokenPath exercises the WorkerTokenPath defaulting
// branch in New. SharedConfigDir is pointed at a writable temp dir so that
// CreateClient succeeds and the returned *Worker can be inspected. With an
// empty WorkerTokenPath, New must substitute configs.DefaultWorkerTokenPath.
func TestNew_DefaultsWorkerTokenPath(t *testing.T) {
	addr := startMockNVCFServer(t)
	tmpDir := t.TempDir()

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   tmpDir,
		WorkerTokenPath:   "", // triggers defaulting branch
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.client.Close()

	if w.config.WorkerTokenPath != configs.DefaultWorkerTokenPath {
		t.Fatalf("expected WorkerTokenPath %q, got %q",
			configs.DefaultWorkerTokenPath, w.config.WorkerTokenPath)
	}
	// SharedConfigDir was explicitly set, so it must be left untouched.
	if w.config.SharedConfigDir != tmpDir {
		t.Fatalf("expected SharedConfigDir %q, got %q", tmpDir, w.config.SharedConfigDir)
	}
}

// TestNew_DefaultsSharedConfigDir exercises the SharedConfigDir defaulting
// branch in New. With an empty SharedConfigDir, New substitutes
// configs.DefaultSharedConfigDir (an absolute path under root that is not
// writable for a non-root test process), so CreateClient fails when it tries
// to create that directory. The defaulting assignment is still executed before
// the failure, which covers the branch. We assert New surfaces the error.
func TestNew_DefaultsSharedConfigDir(t *testing.T) {
	addr := startMockNVCFServer(t)

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   "", // triggers defaulting branch
		WorkerTokenPath:   filepath.Join(t.TempDir(), "worker-token"),
	}

	w, err := New(cfg)
	if err == nil {
		// Extremely unlikely on a developer/CI box, but if the default dir is
		// somehow creatable, the worker is still valid: clean it up and verify
		// the defaulting still happened.
		defer w.client.Close()
		if w.config.SharedConfigDir != configs.DefaultSharedConfigDir {
			t.Fatalf("expected SharedConfigDir %q, got %q",
				configs.DefaultSharedConfigDir, w.config.SharedConfigDir)
		}
		t.Skip("default shared config dir was creatable in this environment; defaulting branch still covered")
	}
}

// TestRun_CreatesMissingTokenDir exercises the directory-creation branch of the
// token-persist callback in Run. WorkerTokenPath points inside a nested
// subdirectory that does not yet exist, so os.Stat returns ErrNotExist and the
// callback must create the directory tree before writing the token.
func TestRun_CreatesMissingTokenDir(t *testing.T) {
	// A restrictive umask must not strip the token's read bits: Run chmods the
	// file after writing so the pylon sidecar can always read it.
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)

	addr := startMockNVCFServer(t)
	tmpDir := t.TempDir()
	// Nested, non-existent subdirectory of the temp dir.
	missingDir := filepath.Join(tmpDir, "a", "b", "c")
	workerTokenPath := filepath.Join(missingDir, "worker-token")

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   tmpDir,
		WorkerTokenPath:   workerTokenPath,
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	runErr := lo.Async(func() error {
		return w.Run(ctx)
	})

	// Poll on observable state (token file existence) instead of sleeping for
	// a fixed amount of time.
	deadline := time.Now().Add(10 * time.Second)
	written := false
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(workerTokenPath); statErr == nil {
			written = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !written {
		t.Fatalf("token file %q was not written within deadline", workerTokenPath)
	}

	// The nested directory must have been created by the callback.
	info, err := os.Stat(missingDir)
	if err != nil {
		t.Fatalf("expected directory %q to be created: %v", missingDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", missingDir)
	}

	content, err := os.ReadFile(workerTokenPath)
	if err != nil {
		t.Fatalf("token file not readable: %v", err)
	}
	if string(content) != testWorkerToken {
		t.Fatalf("expected token %q, got %q", testWorkerToken, string(content))
	}

	// The token must be readable by the pylon sidecar, which runs as a
	// different UID on the shared emptyDir.
	tokenInfo, err := os.Stat(workerTokenPath)
	if err != nil {
		t.Fatalf("token file not found: %v", err)
	}
	if got := tokenInfo.Mode().Perm(); got != 0644 {
		t.Fatalf("expected token file mode 0644, got %o", got)
	}
}

// TestRun_TokenDirCreateFails exercises the error-return of the directory
// creation branch in Run's persist callback. The token path lives under a
// read-only parent directory, so the directory the callback wants to create
// does not exist (os.Stat returns ErrNotExist) and the subsequent
// CreateDirectory (os.MkdirAll) fails with a permission error. Synchronization
// is on the observed "writing worker token to disk" log line, which the
// callback emits before the failing CreateDirectory call.
func TestRun_TokenDirCreateFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses directory permission checks")
	}
	logs := installLogObserver(t)
	addr := startMockNVCFServer(t)
	tmpDir := t.TempDir()

	// Read-only parent: creating a child directory inside it must fail.
	roParent := filepath.Join(tmpDir, "ro")
	if err := os.Mkdir(roParent, 0500); err != nil {
		t.Fatalf("mkdir ro parent: %v", err)
	}
	// Restore writable perms on cleanup so t.TempDir removal succeeds.
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) })

	// dir = roParent/sub does not exist; MkdirAll(roParent/sub) fails EACCES.
	workerTokenPath := filepath.Join(roParent, "sub", "worker-token")

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   tmpDir,
		WorkerTokenPath:   workerTokenPath,
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := lo.Async(func() error { return w.Run(ctx) })

	if !waitForCallbackInvoked(logs) {
		cancel()
		<-runErr
		t.Fatal("persist callback was never invoked within deadline")
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The directory creation failed, so neither the directory nor the token
	// file should exist.
	if _, statErr := os.Stat(workerTokenPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected token file to be absent, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(roParent, "sub")); !os.IsNotExist(statErr) {
		t.Fatalf("expected sub directory to be absent (create should have failed)")
	}
}

// TestRun_TokenWriteFails exercises the error-return of the os.WriteFile branch
// in Run's persist callback. The parent directory already exists (so the
// directory-creation branch is skipped), but the temp target path
// (WorkerTokenPath + ".tmp") is pre-created as a directory, so writing a file
// at that path fails. Synchronization is on the observed callback log line.
func TestRun_TokenWriteFails(t *testing.T) {
	logs := installLogObserver(t)
	addr := startMockNVCFServer(t)
	tmpDir := t.TempDir()

	workerTokenPath := filepath.Join(tmpDir, "worker-token")
	// Make the temp write target a directory so os.WriteFile fails.
	if err := os.Mkdir(workerTokenPath+".tmp", 0755); err != nil {
		t.Fatalf("mkdir tmp target: %v", err)
	}

	cfg := configs.Config{
		NvcfFqdnGrpc:      addr,
		NvcfWorkerToken:   "initial-token",
		FunctionId:        "test-function-id",
		FunctionVersionId: "test-function-version-id",
		NcaId:             "test-nca-id",
		InstanceId:        "test-instance-id",
		SharedConfigDir:   tmpDir,
		WorkerTokenPath:   workerTokenPath,
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErr := lo.Async(func() error { return w.Run(ctx) })

	if !waitForCallbackInvoked(logs) {
		cancel()
		<-runErr
		t.Fatal("persist callback was never invoked within deadline")
	}
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The write failed, so the final token file must not exist.
	if _, statErr := os.Stat(workerTokenPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected token file to be absent after write failure, stat err = %v", statErr)
	}
}
