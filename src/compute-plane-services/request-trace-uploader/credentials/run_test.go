// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package credentials

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &buf
}

// startRun runs the loader's refresh loop on a manual ticker. Each send on the
// returned channel blocks until the loop has taken it, so a second tick proves
// the first reload finished. The returned stop waits for Run to return, and is
// safe to call twice.
func startRun(t *testing.T, loader *Loader) (chan<- time.Time, func()) {
	t.Helper()
	ticks := make(chan time.Time)
	loader.newTicker = func(d time.Duration) (<-chan time.Time, func()) {
		if d != loader.cfg.RefreshInterval {
			t.Errorf("ticker interval = %v, want %v", d, loader.cfg.RefreshInterval)
		}
		return ticks, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loader.Run(ctx)
		close(done)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not return after cancel")
			}
		})
	}
	return ticks, stop
}

func tickTwice(ticks chan<- time.Time) {
	ticks <- time.Now()
	ticks <- time.Now()
}

func TestRunReloadsOnEachTick(t *testing.T) {
	logs := captureLogs(t)
	loader, path := openValid(t)
	ticks, stop := startRun(t, loader)
	defer stop()

	rotated := "rotated-secret-77c3"
	writeSecrets(t, path, `{"`+testIDKey+`":"`+testID+`","`+testSecretKey+`":"`+rotated+`"}`)
	tickTwice(ticks)
	stop()

	if got := mustGet(t, loader, testSecretKey); got != rotated {
		t.Fatalf("secret after tick = %q, want %q", got, rotated)
	}
	output := logs.String()
	if !strings.Contains(output, "credentials changed") || !strings.Contains(output, testSecretKey) {
		t.Fatalf("logs = %q, want a change line naming key %q", output, testSecretKey)
	}
	if strings.Contains(output, rotated) || strings.Contains(output, testSecret) {
		t.Fatalf("logs = %q, contain a credential value", output)
	}
}

func TestRunKeepsLastGoodValuesAndWarnsOnBadRead(t *testing.T) {
	logs := captureLogs(t)
	loader, path := openValid(t)
	ticks, stop := startRun(t, loader)
	defer stop()

	writeSecrets(t, path, `{"`+testIDKey+`":"`+testID+`","`+testSecretKey+`":"half-writ`)
	tickTwice(ticks)
	stop()

	if got := mustGet(t, loader, testSecretKey); got != testSecret {
		t.Fatalf("secret after bad read = %q, want last good %q", got, testSecret)
	}
	output := logs.String()
	if !strings.Contains(output, `"level":"WARN"`) || !strings.Contains(output, "keeping last good credentials") {
		t.Fatalf("logs = %q, want a warning that the last good credentials stay in use", output)
	}
	if strings.Contains(output, "half-writ") || strings.Contains(output, testSecret) {
		t.Fatalf("logs = %q, contain secrets file content", output)
	}
}

func TestRunDoesNotLogWhenNothingChanged(t *testing.T) {
	logs := captureLogs(t)
	loader, _ := openValid(t)
	ticks, stop := startRun(t, loader)
	defer stop()

	tickTwice(ticks)
	stop()

	if logs.Len() != 0 {
		t.Fatalf("logs = %q, want none for an unchanged file", logs.String())
	}
}
