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

package reval

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/getter"
)

func testHTTPGetters() getter.Providers {
	return getter.Providers{{
		Schemes: []string{"http", "https"},
		New:     getter.NewHTTPGetter,
	}}
}

func TestHelmInstallClientLocateChartUsesWorkingDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("chart archive"))
	}))
	t.Cleanup(srv.Close)

	settings, settingsDir, err := initHelmEnv()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(settingsDir) })

	workingDir := t.TempDir()
	client := helmInstallClient{
		Install:     &action.Install{},
		safeGetters: testHTTPGetters(),
		workingDir:  workingDir,
	}

	got, err := client.LocateChart(srv.URL+"/test-chart-1.0.0.tgz", settings)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workingDir, "test-chart-1.0.0.tgz"), got)
}

func TestHelmInstallClientLocateChartCleansFailedDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Ext(r.URL.Path) == ".prov" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("chart archive"))
	}))
	t.Cleanup(srv.Close)

	settings, settingsDir, err := initHelmEnv()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(settingsDir) })

	workingDir := t.TempDir()
	client := helmInstallClient{
		Install:     &action.Install{},
		safeGetters: testHTTPGetters(),
		workingDir:  workingDir,
	}
	client.Verify = true

	_, err = client.LocateChart(srv.URL+"/test-chart-1.0.0.tgz", settings)
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(workingDir, "test-chart-1.0.0.tgz"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestHelmInstallClientLocateChartForwardsRepoCredentials(t *testing.T) {
	const (
		username = "chart-user"
		password = "chart-password"
	)
	chartSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUsername, gotPassword, ok := r.BasicAuth()
		if !ok || gotUsername != username || gotPassword != password {
			http.Error(w, "missing chart credentials", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("chart archive"))
	}))
	t.Cleanup(chartSrv.Close)

	repoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `apiVersion: v1
entries:
  test-chart:
    - version: 1.0.0
      name: test-chart
      urls:
        - %s/test-chart-1.0.0.tgz
`, chartSrv.URL)
	}))
	t.Cleanup(repoSrv.Close)

	settings, settingsDir, err := initHelmEnv()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(settingsDir) })

	workingDir := t.TempDir()
	client := helmInstallClient{
		Install:     &action.Install{},
		safeGetters: testHTTPGetters(),
		workingDir:  workingDir,
	}
	client.RepoURL = repoSrv.URL
	client.Username = username
	client.Password = password
	client.PassCredentialsAll = true
	client.Version = "1.0.0"

	got, err := client.LocateChart("test-chart", settings)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(workingDir, "test-chart-1.0.0.tgz"), got)
}

func Test_isErrHTTPAuthIssue(t *testing.T) {
	assert.False(t, isErrHTTPAuthIssue(nil))
	assert.False(t, isErrHTTPAuthIssue(fmt.Errorf("foo")))
	assert.False(t, isErrHTTPAuthIssue(fmt.Errorf("failed to fetch blah")))
	assert.False(t, isErrHTTPAuthIssue(fmt.Errorf("failed to fetch blah : 400 bad request")))
	assert.True(t, isErrHTTPAuthIssue(fmt.Errorf("failed to fetch blah : 401 invalid creds")))
	assert.True(t, isErrHTTPAuthIssue(fmt.Errorf("failed to fetch blah : 403 unauthorized")))
}

func Test_ngcHostRe(t *testing.T) {
	assert.True(t, ngcHostRe.MatchString("ngc.nvidia.com"))
	assert.True(t, ngcHostRe.MatchString("stg.ngc.nvidia.com"))
	assert.False(t, ngcHostRe.MatchString("stgngc.nvidia.com"))
}
