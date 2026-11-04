// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestLayerPreservesExecutableAndOnlyChown(t *testing.T) {
	value, err := capabilityValue("CHOWN")
	if err != nil {
		t.Fatal(err)
	}
	// Linux revision-2 effective CHOWN; no inheritable or additional permitted bits.
	want := []byte{1, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(value, want) {
		t.Fatalf("capability value = %x, want %x", value, want)
	}
	payload := []byte("\x7fELF\x00executable\xff")
	var first bytes.Buffer
	if err := writeLayer(&first, bytes.NewReader(payload), int64(len(payload)), "/usr/bin/agent", value); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(first.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Name != "usr/bin/agent" || h.Mode != 0o755 || h.Uid != 0 || h.Gid != 0 {
		t.Fatalf("unexpected executable header: %+v", h)
	}
	if got := []byte(h.PAXRecords["SCHILY.xattr.security.capability"]); !bytes.Equal(got, want) {
		t.Fatalf("file capability = %x, want %x", got, want)
	}
	got, err := io.ReadAll(tr)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload = %x, error = %v", got, err)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("unexpected additional entry: %v", err)
	}
	var second bytes.Buffer
	if err := writeLayer(&second, bytes.NewReader(payload), int64(len(payload)), "/usr/bin/agent", value); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("identical inputs produced different layers")
	}
}

func TestRejectsUnsupportedCapabilities(t *testing.T) {
	for _, name := range []string{"", "ALL", "SYS_ADMIN", "CHOWN,SYS_ADMIN"} {
		if _, err := capabilityValue(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"", "/", "agent", "/usr/../agent", "/usr//agent", "/agent\x00"} {
		if err := writeLayer(io.Discard, bytes.NewReader(nil), 0, name, nil); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}
