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

package dsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesContainFindsFixedTextUnderMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	writeRenderedManifest(t, root, "01-nats/templates/statefulset.yaml", "image: docker.io/natsio/reloader:0.23.0\n")
	writeRenderedManifest(t, root, "01-nats/templates/secret.yaml", "# Source: helm-nvcf-nats/templates/nkey-secret.yaml\n")
	writeRenderedManifest(t, root, "02-api/templates/deployment.yaml", "image: docker.io/alpine/k8s:1.36.1\n")

	err := FilesContain(root, "*-nats", []string{
		"docker.io/natsio/reloader:0.23.0",
		"# Source: helm-nvcf-nats/templates/nkey-secret.yaml",
	})
	if err != nil {
		t.Fatalf("find required rendered text: %v", err)
	}
}

func TestFilesContainRestrictsSearchToMatchingDirectories(t *testing.T) {
	root := t.TempDir()
	writeRenderedManifest(t, root, "01-nats/templates/statefulset.yaml", "kind: StatefulSet\n")
	writeRenderedManifest(t, root, "02-api/templates/deployment.yaml", "image: docker.io/alpine/k8s:1.36.1\n")

	err := FilesContain(root, "*-nats", []string{"docker.io/alpine/k8s:1.36.1"})
	if err == nil {
		t.Fatal("expected missing required text error")
	}
	if !strings.Contains(err.Error(), `under directories matching "*-nats"`) ||
		!strings.Contains(err.Error(), "docker.io/alpine/k8s:1.36.1") {
		t.Fatalf("error = %q, want filter and missing text", err)
	}
}

func TestFilesContainSearchesAllFilesWithoutPathFilter(t *testing.T) {
	root := t.TempDir()
	writeRenderedManifest(t, root, "collector.yaml", "kind: OpenTelemetryCollector\n")

	if err := FilesContain(root, "", []string{"kind: OpenTelemetryCollector"}); err != nil {
		t.Fatalf("find required rendered text: %v", err)
	}
}

func TestFilesDoNotContainRejectsMatchedText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-plane", "collector.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create render directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("kind: OpenTelemetryCollector\n"), 0o644); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}

	err := FilesDoNotContain(root, []string{"kind: OpenTelemetryCollector"})
	if err == nil {
		t.Fatal("expected matched text error")
	}
	if !strings.Contains(err.Error(), "collector.yaml") ||
		!strings.Contains(err.Error(), "kind: OpenTelemetryCollector") {
		t.Fatalf("error = %q, want matching file and text", err)
	}
}

func TestFilesDoNotContainRejectsEmptyDirectory(t *testing.T) {
	err := FilesDoNotContain(t.TempDir(), []string{"kind: ServiceMonitor"})
	if err == nil {
		t.Fatal("expected empty render directory error")
	}
	if !strings.Contains(err.Error(), "contains no regular files") {
		t.Fatalf("error = %q, want empty render directory detail", err)
	}
}

func writeRenderedManifest(t *testing.T, root, relativePath, body string) {
	t.Helper()
	filePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("create render directory: %v", err)
	}
	if err := os.WriteFile(filePath, []byte(body), 0o644); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
}
