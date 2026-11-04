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

package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// waitForLogCount polls the observed logs until at least n entries carry the
// given message, or the bounded deadline elapses. It returns the final count so
// the caller can assert without relying on wall-clock timing.
func waitForLogCount(logs *observer.ObservedLogs, msg string, n int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c := logs.FilterMessage(msg).Len(); c >= n {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	return logs.FilterMessage(msg).Len()
}

// waitForKey polls s.NgcApiKey() until it equals want or the bounded deadline
// elapses. It never asserts on wall-clock timing and bounds the wait so the
// test cannot hang if the watcher never updates.
func waitForKey(t *testing.T, s *Secrets, want string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.NgcApiKey() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s.NgcApiKey() == want
}

// writeAndWaitForKey writes content to file and polls s.NgcApiKey() until it
// equals want or the deadline elapses. It rewrites the file on each iteration
// so that an fsnotify event missed during watcher startup (a benign race) is
// retried rather than causing a flaky failure. It bounds the wait so the test
// cannot hang indefinitely and never asserts on wall-clock timing.
func writeAndWaitForKey(t *testing.T, s *Secrets, file, content, want string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if s.NgcApiKey() == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.NgcApiKey() == want
}

// TestNewMissingFile verifies that New returns an error (after exhausting the
// backoff retries) when the secrets file does not exist. The retry loop uses a
// 50ms constant backoff with 10 retries, so this is bounded well under a second
// and does not assert on wall-clock timing.
func TestNewMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.json")

	s, err := New(context.Background(), missing)

	if err == nil {
		t.Fatal("expected error for missing secrets file, got nil")
	}
	if s != nil {
		t.Fatalf("expected nil Secrets on error, got %#v", s)
	}
	if !os.IsNotExist(err) {
		t.Fatalf("expected a not-exist error, got: %v", err)
	}
}

// TestNewMalformedJSON verifies that New surfaces the JSON unmarshal error when
// the secrets file exists but contains invalid JSON.
func TestNewMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(context.Background(), file)

	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if s != nil {
		t.Fatalf("expected nil Secrets on error, got %#v", s)
	}
}

// TestNewValidJSON exercises the happy construction path and the NgcApiKey
// accessor directly. It uses a context that is cancelled immediately so the
// rotateSecrets goroutine takes its ctx.Done() exit branch promptly.
func TestNewValidJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"abc123"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := New(ctx, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Secrets")
	}
	if got := s.NgcApiKey(); got != "abc123" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "abc123")
	}

	// Cancel so the watcher goroutine hits the ctx.Done() branch and returns.
	cancel()
}

// TestLoadNoChange covers the branch in load() where the freshly read secrets
// equal the currently stored value, so the atomic pointer is not re-stored.
// Calling load() twice on an unchanged file exercises both the initial store
// and the no-op path.
func TestLoadNoChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"stable"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file}

	if err := s.load(); err != nil {
		t.Fatalf("first load failed: %v", err)
	}
	first := s.data.Load()
	if first == nil {
		t.Fatal("expected stored data after first load")
	}
	if first.NgcApiKey != "stable" {
		t.Fatalf("NgcApiKey = %q, want %q", first.NgcApiKey, "stable")
	}

	// Second load with identical content must not replace the stored pointer.
	if err := s.load(); err != nil {
		t.Fatalf("second load failed: %v", err)
	}
	if second := s.data.Load(); second != first {
		t.Fatal("unchanged secrets should not be re-stored (pointer changed)")
	}
	if got := s.NgcApiKey(); got != "stable" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "stable")
	}
}

// TestLoadUpdatesOnChange covers the branch where new content differs from the
// stored value and the atomic pointer is swapped, invoked directly via load()
// without relying on the fsnotify watcher.
func TestLoadUpdatesOnChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file}
	if err := s.load(); err != nil {
		t.Fatalf("first load failed: %v", err)
	}
	old := s.data.Load()

	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"new"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.load(); err != nil {
		t.Fatalf("second load failed: %v", err)
	}

	if updated := s.data.Load(); updated == old {
		t.Fatal("changed secrets should be re-stored (pointer unchanged)")
	}
	if got := s.NgcApiKey(); got != "new" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "new")
	}
}

// TestLoadMalformedJSONDirect verifies load() returns the unmarshal error
// directly when the file content is not valid JSON, independent of New.
func TestLoadMalformedJSONDirect(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte("[]not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file}
	if err := s.load(); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// TestRotateSecretsWatcherBranches drives the rotateSecrets goroutine through
// several of its watcher event branches:
//   - an unrelated file event in the watched directory, which must be filtered
//     out via the filename/op continue branch and leave the cache unchanged;
//   - a malformed write to the secrets file, which exercises the load-error
//     continue branch and must not corrupt the previously loaded value;
//   - a valid update, which exercises the successful rotation path;
//   - context cancellation, which exercises the ctx.Done() return branch.
//
// It uses bounded polling rather than fixed sleeps for the rotation assertions.
func TestRotateSecretsWatcherBranches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"initial"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s, err := New(ctx, file)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := s.NgcApiKey(); got != "initial" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "initial")
	}

	// Unrelated file in the watched directory: filename does not match the
	// secrets file base name, so the event must be ignored (continue branch).
	unrelated := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(unrelated, []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First valid rotation so we know the watcher pipeline is live before we
	// probe the malformed path.
	if !writeAndWaitForKey(t, s, file, `{"NGC_API_KEY":"rotated"}`, "rotated") {
		t.Fatalf("secret was not rotated; NgcApiKey() = %q, want %q", s.NgcApiKey(), "rotated")
	}

	// Malformed write to the secrets file drives the load-error continue branch
	// in the event handler. We do not assert the cached value synchronously
	// here: the watcher processes the event asynchronously, so a same-instant
	// read would pass only because the event has not been dequeued yet. The
	// subsequent rotation to "final" is the real signal -- it can only be
	// observed if the loop survived the malformed event and kept running.
	// TestWatchLoopLoadErrorContinue covers the continue branch deterministically.
	if err := os.WriteFile(file, []byte("{broken json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Restore valid content; the cache settles on the final value, which proves
	// the loop continued past the malformed write rather than exiting.
	if !writeAndWaitForKey(t, s, file, `{"NGC_API_KEY":"final"}`, "final") {
		t.Fatalf("secret did not settle; NgcApiKey() = %q, want %q", s.NgcApiKey(), "final")
	}

	// Cancel to drive the ctx.Done() return branch in rotateSecrets.
	cancel()
}

// TestWatchLoopPollFallback exercises the pollTicker branch of watchLoop. It
// drives watchLoop directly with a short per-instance poll interval, then
// updates the file content. The poll-driven load() should pick up the change
// even if the fsnotify event is missed. Using the per-instance pollInterval
// field avoids mutating shared global state under the race detector.
func TestWatchLoopPollFallback(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"poll-initial"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file, pollInterval: 20 * time.Millisecond}
	if err := s.load(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if got := s.NgcApiKey(); got != "poll-initial" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "poll-initial")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer watcher.Close()
	if err := watcher.Add(dir); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.watchLoop(ctx, watcher)
		close(done)
	}()

	// Update content once. The fast poll guarantees the load happens even if the
	// fsnotify event is missed.
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"poll-updated"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !waitForKey(t, s, "poll-updated") {
		t.Fatalf("poll did not pick up update; NgcApiKey() = %q, want %q", s.NgcApiKey(), "poll-updated")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchLoop did not return after ctx cancel")
	}
}

// TestWatchLoopLoadErrorContinue deterministically exercises the load-error
// continue branch. It drives watchLoop directly with a caller-supplied watcher
// and a tiny pollInterval while the secrets file holds malformed JSON, captures
// watchLoop's error logs to confirm the failed loads were handled, then restores
// valid content before cancelling so the loop is not parked in load() backoff
// (which previously made the post-cancel return time-sensitive).
func TestWatchLoopLoadErrorContinue(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"kept"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file, pollInterval: 20 * time.Millisecond}
	if err := s.load(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}

	// Capture watchLoop's error logs so the load-error branch is detected
	// deterministically rather than guessed at with sleeps. Restored after the test.
	core, logs := observer.New(zapcore.ErrorLevel)
	t.Cleanup(zap.ReplaceGlobals(zap.New(core)))

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer watcher.Close()
	if err := watcher.Add(dir); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.watchLoop(ctx, watcher)
		close(done)
	}()

	// Hold malformed content until load() has failed at least twice (the Write
	// event and the periodic poll both call it), confirming the continue branch
	// ran. Each malformed load() exhausts its backoff before returning the error.
	if err := os.WriteFile(file, []byte("{still broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := waitForLogCount(logs, "failed to load secrets file", 2, 10*time.Second); got < 2 {
		t.Fatalf("expected at least 2 load failures, observed %d", got)
	}

	// The malformed content must not clear the cached value. This is now
	// deterministic: the logs above prove load() actually ran and failed.
	if got := s.NgcApiKey(); got != "kept" {
		t.Fatalf("malformed content must not clear the cache; NgcApiKey() = %q, want %q", got, "kept")
	}

	// Restore valid content. Observing it proves the loop survived the load
	// errors (took the continue branch rather than exiting), and it lets any
	// queued events load() successfully so the loop is not parked in backoff
	// when we cancel, keeping the ctx.Done() return prompt.
	if !writeAndWaitForKey(t, s, file, `{"NGC_API_KEY":"recovered"}`, "recovered") {
		t.Fatalf("loop did not survive load errors; NgcApiKey() = %q, want %q", s.NgcApiKey(), "recovered")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchLoop did not return after ctx cancel")
	}
}

// TestWatchLoopWatcherClosed exercises the channel-closed return branches in
// watchLoop. Closing the watcher closes its Events and Errors channels, so the
// loop must observe a not-ok receive and return promptly.
func TestWatchLoopWatcherClosed(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file}
	if err := s.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if err := watcher.Add(dir); err != nil {
		t.Fatalf("watcher.Add: %v", err)
	}

	done := make(chan struct{})
	go func() {
		s.watchLoop(context.Background(), watcher)
		close(done)
	}()

	// Closing the watcher closes the Events/Errors channels, driving a not-ok
	// receive and the corresponding return branch.
	if err := watcher.Close(); err != nil {
		t.Fatalf("watcher.Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchLoop did not return after watcher close")
	}
}

// TestRotateSecretsWatcherAddError exercises the early-return path in
// rotateSecrets when the watched directory cannot be added to the fsnotify
// watcher because it does not exist. New still succeeds (load reads the file
// before it is removed), and the goroutine returns without panicking.
func TestRotateSecretsWatcherAddError(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(subdir, "secrets.json")
	if err := os.WriteFile(file, []byte(`{"NGC_API_KEY":"present"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Secrets{secretsFilePath: file}
	if err := s.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Remove the directory so watcher.Add(dir) fails inside rotateSecrets.
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// rotateSecrets must return promptly via the watcher.Add error branch.
	done := make(chan struct{})
	go func() {
		s.rotateSecrets(ctx)
		close(done)
	}()

	select {
	case <-done:
		// returned via the add-error branch as expected
	case <-time.After(5 * time.Second):
		t.Fatal("rotateSecrets did not return after watcher.Add failure")
	}

	if got := s.NgcApiKey(); got != "present" {
		t.Fatalf("NgcApiKey() = %q, want %q", got, "present")
	}
}
