// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func gunzip(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestJSONLFileWritesGzipLinesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transport.jsonl.gz")
	writer, err := CreateJSONLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"a":1}`, `{"b":2}`} {
		if err := writer.Write(Record{Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("transport visible before Close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := gunzip(t, path); got != "{\"a\":1}\n{\"b\":2}\n" {
		t.Fatalf("transport = %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory has %d entries (%v), want only the transport", len(entries), err)
	}
}

func TestJSONLFileAbortLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	writer, err := CreateJSONLFile(filepath.Join(dir, "transport.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(Record{Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("directory has %d entries after Abort, want none", len(entries))
	}
}

func TestRejectFileCreatesNothingWhenUnused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rejects")
	rejects := NewRejectFile(dir)
	committed, err := rejects.Commit("segment-id")
	if err != nil || committed {
		t.Fatalf("Commit() = %v, %v, want nothing to commit", committed, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reject directory created with nothing rejected: %v", err)
	}
}

func TestRejectFileCommitsUnderSegmentID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rejects")
	rejects := NewRejectFile(dir)
	if _, err := rejects.Write([]byte("bad line\n")); err != nil {
		t.Fatal(err)
	}
	committed, err := rejects.Commit("abc123")
	if err != nil || !committed {
		t.Fatalf("Commit() = %v, %v", committed, err)
	}
	path := filepath.Join(dir, "abc123.jsonl.gz")
	if got := gunzip(t, path); got != "bad line\n" {
		t.Fatalf("reject artifact = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %v, want 0600 because rejects can hold user data", mode)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the artifact", len(entries))
	}
}

func TestRejectFileDiscardRemovesPartialArtifact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rejects")
	rejects := NewRejectFile(dir)
	if _, err := rejects.Write([]byte("bad line\n")); err != nil {
		t.Fatal(err)
	}
	if err := rejects.Discard(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("directory has %d entries after Discard, want none", len(entries))
	}
}

func TestRejectFileRejectsUnsafeName(t *testing.T) {
	for _, name := range []string{"", "../x", "a/b", ".", ".."} {
		rejects := NewRejectFile(t.TempDir())
		if _, err := rejects.Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := rejects.Commit(name); err == nil {
			t.Errorf("Commit(%q) accepted an unsafe name", name)
		}
		if err := rejects.Discard(); err != nil {
			t.Fatal(err)
		}
	}
}
