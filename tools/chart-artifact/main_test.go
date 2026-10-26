// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name, content string
	kind          byte
}

func entries() []entry {
	return []entry{
		{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 1.0.0\n", tar.TypeReg},
		{"demo/templates/config.yaml", "apiVersion: v1\nkind: ConfigMap\n", tar.TypeReg},
		{"demo/values.yaml", "replicas: 1\n", tar.TypeReg},
	}
}

func archive(t *testing.T, contents []entry, timestamp int64) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "chart.tgz")
	f, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	gz.ModTime = time.Unix(timestamp, 0)
	tw := tar.NewWriter(gz)
	for _, e := range contents {
		header := &tar.Header{Name: e.name, Typeflag: e.kind, Mode: 0644, ModTime: time.Unix(timestamp, 0)}
		if e.kind == tar.TypeReg {
			header.Size = int64(len(e.content))
		}
		if e.kind == tar.TypeSymlink || e.kind == tar.TypeLink {
			header.Linkname = "../../outside"
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if e.kind == tar.TypeReg {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, closer := range []io.Closer{tw, gz, f} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return filename
}

func command(t *testing.T, wantError string, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(args, &stdout, &stderr)
	if wantError == "" && err != nil {
		t.Fatalf("run(%q): %v", args, err)
	}
	if wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
		t.Fatalf("run(%q): got %v, want %q", args, err, wantError)
	}
	return stdout.String()
}

func TestComparisonIgnoresArchiveMetadataAndOrder(t *testing.T) {
	src := archive(t, entries(), 100)
	reordered := entries()
	slices.Reverse(reordered)
	dst := archive(t, reordered, 200)
	srcBytes, _ := os.ReadFile(src)
	dstBytes, _ := os.ReadFile(dst)
	if bytes.Equal(srcBytes, dstBytes) {
		t.Fatal("fixture archives must differ byte for byte")
	}
	for range 2 { // A retry against the same existing package remains successful.
		command(t, "", "compare", "--name", "demo", "--version", "1.0.0", src, dst)
	}
	sourceOutput := command(t, "", "validate", "--name", "demo", "--version", "1.0.0", src)
	destOutput := command(t, "", "validate", "--name", "demo", "--version", "1.0.0", dst)
	if sourceOutput != destOutput {
		t.Fatalf("canonical digests differ: %s != %s", sourceOutput, destOutput)
	}
}

func TestContentDifferences(t *testing.T) {
	for _, name := range []string{"changed file", "removed file", "extra file", "renamed file"} {
		t.Run(name, func(t *testing.T) {
			src := archive(t, entries(), 100)
			destination := entries()
			switch name {
			case "changed file":
				destination[2].content = "replicas: 2\n"
			case "removed file":
				destination = destination[:2]
			case "extra file":
				destination = append(destination, entry{"demo/extra", "extra", tar.TypeReg})
			case "renamed file":
				destination[2].name = "demo/renamed.yaml"
			}
			dst := archive(t, destination, 200)
			command(t, "chart content differs", "compare", "--name", "demo", "--version", "1.0.0", src, dst)
		})
	}
}

func TestInvalidArchives(t *testing.T) {
	cases := []struct {
		name, message string
		change        func([]entry) []entry
	}{
		{"wrong name", "does not match chart name", func(e []entry) []entry { e[0].content = "name: other\nversion: 1.0.0\n"; return e }},
		{"wrong version", "expected demo:1.0.0", func(e []entry) []entry { e[0].content = "name: demo\nversion: 2.0.0\n"; return e }},
		{"no metadata", "no root Chart.yaml", func(e []entry) []entry { return e[1:] }},
		{"malformed YAML", "parse Chart.yaml", func(e []entry) []entry { e[0].content = "[broken"; return e }},
		{"duplicate YAML key", "parse Chart.yaml", func(e []entry) []entry { e[0].content += "name: other\n"; return e }},
		{"multiple documents", "exactly one YAML document", func(e []entry) []entry { e[0].content += "---\nname: other\n"; return e }},
		{"missing version", "nonempty chart name and version", func(e []entry) []entry { e[0].content = "name: demo\n"; return e }},
		{"duplicate path", "duplicate archive path", func(e []entry) []entry { return append(e, e[1]) }},
		{"multiple roots", "exactly one root", func(e []entry) []entry { e[2].name = "other/values.yaml"; return e }},
		{"traversal", "unsafe archive path", func(e []entry) []entry { e[2].name = "demo/../../outside"; return e }},
		{"absolute", "unsafe archive path", func(e []entry) []entry { e[2].name = "/demo/values.yaml"; return e }},
		{"backslash", "unsafe archive path", func(e []entry) []entry { e[2].name = "demo/..\\outside"; return e }},
		{"symlink", "unsupported archive entry", func(e []entry) []entry { e[2].kind = tar.TypeSymlink; return e }},
		{"hard link", "unsupported archive entry", func(e []entry) []entry { e[2].kind = tar.TypeLink; return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := archive(t, tc.change(entries()), 100)
			good := archive(t, entries(), 100)
			command(t, tc.message, "validate", "--name", "demo", "--version", "1.0.0", bad)
			command(t, tc.message, "compare", "--name", "demo", "--version", "1.0.0", good, bad)
		})
	}
}

func TestUnreadableOrCorruptArchives(t *testing.T) {
	for _, scenario := range []string{"missing", "not gzip", "truncated", "checksum"} {
		t.Run(scenario, func(t *testing.T) {
			file := archive(t, entries(), 100)
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			message := "source "
			switch scenario {
			case "missing":
				file += ".missing"
			case "not gzip":
				data = []byte("not gzip")
			case "truncated":
				data = data[:len(data)/2]
			case "checksum":
				data[len(data)-8] ^= 0xff
				message = "gzip: invalid checksum"
			}
			if scenario != "missing" {
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			command(t, message, "validate", "--name", "demo", "--version", "1.0.0", file)
			command(t, "destination ", "compare", "--name", "demo", "--version", "1.0.0", archive(t, entries(), 100), file)
		})
	}
}

func TestMetadataAndArguments(t *testing.T) {
	file := archive(t, entries(), 100)
	metadata := filepath.Join(t.TempDir(), "Chart.yaml")
	if err := os.WriteFile(metadata, []byte("name: 'demo' # chart name\nversion: \"1.0.0\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, "", "validate", "--metadata", metadata, file)
	command(t, "expected demo:2.0.0", "validate", "--name", "demo", "--version", "2.0.0", file)
	command(t, "expected identity is required", "validate", file)
	command(t, "not both", "validate", "--metadata", metadata, "--name", "demo", file)
	command(t, "requires 2 archive", "compare", "--metadata", metadata, file)
	command(t, "usage:")
}

func TestDiscoveryStopsAtChartRoot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"first", "first/charts/dependency", "nested/second", ".cache/ignored", "node_modules/ignored", "testdata/ignored"} {
		chart := filepath.Join(root, dir)
		if err := os.MkdirAll(chart, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("name: fixture\nversion: 1.0.0\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output := command(t, "", "discover", root)
	want := filepath.Join(root, "first") + "\n" + filepath.Join(root, "nested/second") + "\n"
	if output != want {
		t.Fatalf("discovered charts %q, want %q", output, want)
	}
	command(t, "discover charts under", "discover", filepath.Join(root, "missing"))
}

func TestRejectsTrailingNonpadding(t *testing.T) {
	file := archive(t, entries(), 100)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte("hidden trailing content")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	command(t, "nonpadding data", "validate", "--name", "demo", "--version", "1.0.0", file)
}

func TestDependencyRepositories(t *testing.T) {
	// Construct synthetic credentials so secret scanners do not mistake a
	// literal authenticated URI fixture for a committed credential.
	credentialURL := &url.URL{Scheme: "https", Host: "charts.example", Path: "/stable", User: url.UserPassword("fixture-user", "fixture-password")}
	metadata := filepath.Join(t.TempDir(), "Chart.yaml")
	for _, tc := range []struct{ repository, wantError string }{
		{"https://charts.example/stable", ""},
		{"@local-alias", "credential-free URL"},
		{credentialURL.String(), "credential-free URL"},
	} {
		t.Run(tc.repository, func(t *testing.T) {
			content := "dependencies:\n- repository: '" + tc.repository + "'\n- repository: '" + tc.repository + "'\n- repository: file://charts/local\n- repository: oci://registry.example/charts\n"
			if err := os.WriteFile(metadata, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			output := command(t, tc.wantError, "repositories", metadata)
			if tc.wantError == "" && (strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, " "+tc.repository+"\n")) {
				t.Fatalf("unexpected repository registration: %q", output)
			}
		})
	}
}
