// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package registry_test

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/test/registry/registry"
)

var silentLogger = log.New(io.Discard, "", 0)

// ── NewImageRegistryServer ────────────────────────────────────────────────────

func TestNewImageRegistryServer_ReturnsServer(t *testing.T) {
	srv, err := registry.NewImageRegistryServer(silentLogger, "localhost:5000")
	require.NoError(t, err)
	require.NotNil(t, srv)
	require.NotNil(t, srv.Handler)
}

func TestNewImageRegistryServer_HandlerResponds(t *testing.T) {
	srv, err := registry.NewImageRegistryServer(silentLogger, "localhost:5000")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// OCI registry /v2/ endpoint should respond 200 (or 401 for auth-required registries).
	resp, err := http.Get(ts.URL + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized)
}

// ── PushPublicImages ──────────────────────────────────────────────────────────

func TestPushPublicImages_PushesToLocalRegistry(t *testing.T) {
	srv, err := registry.NewImageRegistryServer(silentLogger, "localhost")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	tags := []string{"test-image:v1.0", "another-image:latest"}
	err = registry.PushPublicImages(host, tags)
	require.NoError(t, err)
}

func TestPushPublicImages_EmptyList(t *testing.T) {
	srv, err := registry.NewImageRegistryServer(silentLogger, "localhost")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "http://")
	err = registry.PushPublicImages(host, []string{})
	require.NoError(t, err)
}

// ── NewTestHelmRepoServer ─────────────────────────────────────────────────────

func TestNewTestHelmRepoServer_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "mypassword")
	require.NoError(t, err)
	require.NotNil(t, srv)
}

func TestNewTestHelmRepoServer_IndexYAML_NoAuth(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "secret")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Without auth → should be rejected.
	resp, err := http.Get(ts.URL + "/index.yaml")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestNewTestHelmRepoServer_IndexYAML_WrongPassword(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "correct-pw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/index.yaml", nil)
	req.SetBasicAuth("$oauthtoken", "wrong-pw")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestNewTestHelmRepoServer_IndexYAML_ValidAuth_EmptyIndex(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "mypw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/index.yaml", nil)
	req.SetBasicAuth("$oauthtoken", "mypw")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestNewTestHelmRepoServer_ChartTGZ_NoAuth(t *testing.T) {
	// Copy real chart so the route exists in the mux.
	src := filepath.Join("..", "..", "testchart", "multi-node-secrets-test-0.3.4.tgz")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Skip("testchart not present, skipping")
	}
	dir := t.TempDir()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "multi-node-secrets-test-0.3.4.tgz"), data, 0o644))

	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "mypw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/multi-node-secrets-test-0.3.4.tgz")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestNewTestHelmRepoServer_ChartTGZ_WithAuth_NotFound(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "mypw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/mychart-0.1.0.tgz", nil)
	req.SetBasicAuth("$oauthtoken", "mypw")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestNewTestHelmRepoServer_V2Endpoint(t *testing.T) {
	dir := t.TempDir()
	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "pw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v2/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestPushPublicImages_PushFails_ReturnsError(t *testing.T) {
	// Push to a port that is not listening → remote.Push should fail.
	// Use a port that is very unlikely to be in use.
	err := registry.PushPublicImages("localhost:1", []string{"test-image:v1"})
	require.Error(t, err)
}

func TestNewTestHelmRepoServer_NonExistentDir_ReturnsError(t *testing.T) {
	_, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", "/does/not/exist/xyz", "pw")
	require.Error(t, err)
}

func TestNewTestHelmRepoServer_MalformedTGZName_ReturnsError(t *testing.T) {
	// A .tgz file whose name doesn't match "{name}-{semver}.tgz" should cause an error.
	dir := t.TempDir()
	badFile := filepath.Join(dir, "invalid.tgz") // no version component
	require.NoError(t, os.WriteFile(badFile, []byte("fake content"), 0o644))

	_, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "pw")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid.tgz")
}

func TestPushPublicImages_InvalidReference_ReturnsError(t *testing.T) {
	// A reference with a malformed name should cause ParseReference to fail.
	err := registry.PushPublicImages("localhost:9999", []string{"INVALID IMAGE NAME WITH SPACES"})
	require.Error(t, err)
}

func TestNewTestHelmRepoServer_WithChartDirectory(t *testing.T) {
	// Put a chart directory (not a .tgz) into testdataDir; the server should
	// package it into a gzip tarball on the fly and expose it as {name}-1.0.0.tgz.
	dir := t.TempDir()

	// Create a minimal chart directory with a Chart.yaml and a template.
	chartDir := filepath.Join(dir, "myfakechart")
	require.NoError(t, os.MkdirAll(filepath.Join(chartDir, "templates"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(chartDir, "Chart.yaml"),
		[]byte("apiVersion: v2\nname: myfakechart\nversion: 1.0.0\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(chartDir, "templates", "deploy.yaml"),
		[]byte("# empty template\n"),
		0o644,
	))
	// Add an exp*.json file to exercise the filter branch in filepath.Walk.
	require.NoError(t, os.WriteFile(
		filepath.Join(chartDir, "expdata.json"),
		[]byte(`{"expected": true}`),
		0o644,
	))

	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "pw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Index should list the constructed chart.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/index.yaml", nil)
	req.SetBasicAuth("$oauthtoken", "pw")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "myfakechart")

	// Download the constructed tgz (no-auth should be 401).
	resp2, err := http.Get(ts.URL + "/myfakechart-1.0.0.tgz")
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp2.StatusCode)

	// Download with auth should be 200.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/myfakechart-1.0.0.tgz", nil)
	req3.SetBasicAuth("$oauthtoken", "pw")
	resp3, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	defer resp3.Body.Close()
	assert.Equal(t, http.StatusOK, resp3.StatusCode)
}

func TestNewTestHelmRepoServer_WithRealChart(t *testing.T) {
	// Copy the testchart tgz from the project testchart directory.
	src := filepath.Join("..", "..", "testchart", "multi-node-secrets-test-0.3.4.tgz")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Skip("testchart not present, skipping")
	}

	dir := t.TempDir()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	destFile := filepath.Join(dir, "multi-node-secrets-test-0.3.4.tgz")
	require.NoError(t, os.WriteFile(destFile, data, 0o644))

	srv, err := registry.NewTestHelmRepoServer(silentLogger, "127.0.0.1:8282", dir, "mypw")
	require.NoError(t, err)

	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// Index should contain the chart.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/index.yaml", nil)
	req.SetBasicAuth("$oauthtoken", "mypw")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "multi-node-secrets-test")

	// The chart tgz should be downloadable.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/multi-node-secrets-test-0.3.4.tgz", nil)
	req2.SetBasicAuth("$oauthtoken", "mypw")
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}
