// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
)

// capture wraps a backend and keeps the records of every file it is asked to
// submit, read at submit time, because the pipeline removes its transport
// files once an export is confirmed.
type capture struct {
	backend.Client
	t     testing.TB
	mu    sync.Mutex
	lines []string
}

func newCapture(t testing.TB, client backend.Client) *capture {
	return &capture{Client: client, t: t}
}

func (c *capture) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	for _, file := range request.Transports() {
		lines := readGzipLines(c.t, file.Path)
		c.mu.Lock()
		c.lines = append(c.lines, lines...)
		c.mu.Unlock()
	}
	return c.Client.Submit(ctx, request)
}

func (c *capture) exported() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...)
}

func readGzipLines(t testing.TB, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open submitted file: %v", err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("read submitted file: %v", err)
	}
	var lines []string
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read submitted file: %v", err)
	}
	return lines
}

// loadEnv loads configuration from env.
func loadEnv(t *testing.T, env map[string]string) config.Config {
	t.Helper()
	cfg, _, err := config.Load(func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

// spoolEnv is the minimal environment for a spool directory.
func spoolEnv(t *testing.T, dir string, extra map[string]string) map[string]string {
	t.Helper()
	secrets := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		config.EnvSourceDir:   dir,
		config.EnvBackend:     string(config.BackendDebug),
		config.EnvSecretsFile: secrets,
	}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

// captureLogs sends the default logger to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&lockedWriter{w: &buf}, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
