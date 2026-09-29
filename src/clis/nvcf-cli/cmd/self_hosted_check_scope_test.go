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

package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvcf-cli/internal/selfhosted"
)

// runCheckRecording runs `self-hosted check` in ModeSingle with the given
// flags and returns the namespaces the stale-namespace probe was asked about
// and how many times SIS was contacted.
func runCheckRecording(t *testing.T, args ...string) (namespaces []string, sisHits int64) {
	t.Helper()
	resetCheckFlags(t)
	var hits atomic.Int64
	sis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sis.Close)
	t.Setenv("NVCF_ICMS_URL", sis.URL)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")

	var mu sync.Mutex
	prev := newStaleNamespaceProberForSelfHosted
	newStaleNamespaceProberForSelfHosted = func() selfhosted.StaleNamespaceProber {
		return func(_ context.Context, _ string, ns []string) ([]selfhosted.StaleNamespace, error) {
			mu.Lock()
			defer mu.Unlock()
			namespaces = append(namespaces, ns...)
			return nil, nil
		}
	}
	t.Cleanup(func() { newStaleNamespaceProberForSelfHosted = prev })

	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetArgs(append([]string{"self-hosted", "check", "--skip-cluster-validation", "--json"}, args...))
	_ = rootCmd.Execute()
	return namespaces, hits.Load()
}

// Each role's namespaces are scanned only when that role runs. Merging both in
// for a single-role run reported a stuck kai-scheduler on --control-plane.
func TestCheck_StaleNamespacesFollowTheRequestedRoles(t *testing.T) {
	ns, _ := runCheckRecording(t, "--control-plane")
	assert.Contains(t, ns, "vault-system")
	assert.NotContains(t, ns, "kai-scheduler", "--control-plane must not scan compute namespaces")

	ns, _ = runCheckRecording(t, "--compute-plane")
	assert.Contains(t, ns, "kai-scheduler")
	assert.NotContains(t, ns, "vault-system", "--compute-plane must not scan control-plane namespaces")

	ns, _ = runCheckRecording(t, "--all")
	assert.Contains(t, ns, "vault-system")
	assert.Contains(t, ns, "kai-scheduler", "--all scans both")
}

// A bare --pre skips SIS, which is not up before install; an explicit --all or
// --compute-plane still asks for it; --control-plane never contacts it.
func TestCheck_SISReachabilityScope(t *testing.T) {
	_, hits := runCheckRecording(t, "--pre")
	assert.Zero(t, hits, "--pre alone is pre-install")
	_, hits = runCheckRecording(t, "--pre", "--all")
	assert.NotZero(t, hits, "--all asks for every category, --pre included")
	_, hits = runCheckRecording(t, "--pre", "--compute-plane")
	assert.NotZero(t, hits)
	_, hits = runCheckRecording(t, "--control-plane")
	assert.Zero(t, hits, "SIS is a compute-plane check")
}

// --local-only overrides the required scope flag; the run says so on stderr.
func TestCheck_LocalOnlyNotesSkippedClusterChecks(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	var stderr bytes.Buffer
	rootCmd.SetErr(&stderr)
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	rootCmd.SetArgs([]string{"self-hosted", "check", "--all", "--local-only", "--json"})
	_ = rootCmd.Execute()
	assert.Contains(t, stderr.String(), "--local-only runs the local host checks only")
}

// An interrupted run reports the interrupt, not a verdict: with the validator
// cut short the results would otherwise read as a failure, or as a pass.
func TestCheck_InterruptExits130(t *testing.T) {
	resetCheckFlags(t)
	t.Setenv("NVCF_CLI_SELFHOSTED_SKIP_INOTIFY", "1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetOut(&bytes.Buffer{})
	t.Cleanup(func() { rootCmd.SetErr(nil); rootCmd.SetOut(nil) })
	// Cobra keeps a subcommand's context from an earlier Execute, so set it
	// on the command that runs.
	selfHostedCheckCmd.SetContext(ctx)
	t.Cleanup(func() { selfHostedCheckCmd.SetContext(context.Background()) })
	rootCmd.SetArgs([]string{"self-hosted", "check", "--all", "--local-only", "--json"})
	err := rootCmd.Execute()

	var exitErr *ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 130, exitErr.Code)
}
