// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// TestHTTPRegistryRetries checks that only transient registry failures retry.
func TestHTTPRegistryRetries(t *testing.T) {
	for _, tt := range []struct {
		name      string
		statuses  []int
		wantCalls int
		wantError bool
	}{
		{"temporary then success", []int{503, 200}, 2, false},
		{"rate limit then success", []int{429, 200}, 2, false},
		{"persistent failure", []int{502, 502, 502}, 3, true},
		{"not found", []int{404}, 1, true},
		{"unauthorized", []int{401}, 1, true},
		{"forbidden", []int{403}, 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls >= len(tt.statuses) {
					t.Error("unexpected extra request")
					w.WriteHeader(500)
					return
				}
				status := tt.statuses[calls]
				calls++
				w.WriteHeader(status)
				fmt.Fprint(w, `{"license":"MIT"}`)
			}))
			defer server.Close()
			body, status, err := httpGetStringWithStatus(server.URL, 5*time.Second, nil)
			if calls != tt.wantCalls || (err != nil) != tt.wantError {
				t.Fatalf("calls=%d, err=%v", calls, err)
			}
			if !tt.wantError && (body != `{"license":"MIT"}` || status != 200) {
				t.Fatalf("body=%q status=%d", body, status)
			}
		})
	}
}

// lookupTransport supplies deterministic responses without a live registry.
type lookupTransport func(*http.Request) (*http.Response, error)

// RoundTrip delegates to the test's response sequence.
func (f lookupTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestHTTPRetryClearsPriorResponse verifies that a transport failure does not
// return the status or partial body from an earlier attempt.
func TestHTTPRetryClearsPriorResponse(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			old := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: lookupTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return nil, errors.New("permanent transport failure")
				}
				body := io.NopCloser(io.MultiReader(strings.NewReader("partial response"), iotest.ErrReader(io.ErrUnexpectedEOF)))
				return &http.Response{StatusCode: status, Body: body}, nil
			})}
			t.Cleanup(func() { http.DefaultClient = old })
			body, gotStatus, err := httpGetBytesWithStatus("https://registry.example.test", time.Second, nil)
			if err == nil || calls != 2 || gotStatus != 0 || len(body) != 0 {
				t.Fatalf("calls=%d status=%d body=%q err=%v", calls, gotStatus, body, err)
			}
		})
	}
}

// TestLookupTimeoutBudget checks that retries share the original timeout budget.
func TestLookupTimeoutBudget(t *testing.T) {
	calls := 0
	err := retryLookup(20*time.Millisecond, func(remaining time.Duration) error {
		calls++
		time.Sleep(remaining)
		return networkLookupError(context.DeadlineExceeded)
	})
	if err == nil || calls != 1 {
		t.Fatalf("unbounded retry: calls=%d err=%v", calls, err)
	}
}

// TestGoLookupDiagnosticsDoNotExposeCredentials checks classification and safe diagnostics.
func TestGoLookupDiagnosticsDoNotExposeCredentials(t *testing.T) {
	credentialURL := (&url.URL{
		Scheme:   "https",
		Host:     "registry.example.test",
		User:     url.UserPassword("fixture-user", "fixture-password"),
		RawQuery: "token=fixture-query",
	}).String()
	for _, tt := range []struct {
		text  string
		retry bool
	}{
		{"proxy: 503 Service Unavailable " + credentialURL, true},
		{"checksum mismatch\nproxy: 503 Service Unavailable", false},
		{"404 Not Found " + credentialURL, false},
		{"401 Unauthorized " + credentialURL, false},
		{"updates to go.mod needed; run go mod tidy", false},
		{"read: connection reset by peer", true},
	} {
		err := goLookupError(errors.New("exit status 1"), tt.text)
		if err.temporary != tt.retry {
			t.Fatalf("retry classification for %q = %t", tt.text, err.temporary)
		}
		if strings.Contains(err.Error(), "fixture-password") || strings.Contains(err.Error(), "fixture-query") || strings.Contains(err.Error(), "https://") {
			t.Fatal("unsafe diagnostics")
		}
	}
}

// TestGoDownloadRetriesAndReportsUnavailable checks module resolution and unresolved reporting.
func TestGoDownloadRetriesAndReportsUnavailable(t *testing.T) {
	for _, mode := range []string{"recover", "persistent", "permanent", "missing-license"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			moduleDir := filepath.Join(dir, "module")
			writeTestFile(t, filepath.Join(moduleDir, "LICENSE"), "MIT License\n")
			if mode == "missing-license" {
				if err := os.Remove(filepath.Join(moduleDir, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			}
			writeTestFile(t, filepath.Join(dir, "go.sum"), "example.test/dependency v1.2.3 h1:fixture\n")
			metadata, _ := json.Marshal(map[string]string{"Dir": moduleDir})
			bin := filepath.Join(dir, "bin")
			writeTestFile(t, filepath.Join(bin, "go"), `#!/bin/sh
count=0
if [ -f "$LOOKUP_COUNTER" ]; then read -r count < "$LOOKUP_COUNTER"; fi
count=$((count+1))
printf '%s\n' "$count" > "$LOOKUP_COUNTER"
case "$LOOKUP_MODE" in
 persistent) printf '503 Service Unavailable\n' >&2; exit 1;;
 permanent) printf '401 Unauthorized\n' >&2; exit 1;;
 recover) if [ "$count" -eq 1 ]; then printf '503 Service Unavailable\n' >&2; exit 1; fi;;
esac
printf '%s\n' "$LOOKUP_METADATA"
`)
			if err := os.Chmod(filepath.Join(bin, "go"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LOOKUP_COUNTER", filepath.Join(dir, "count"))
			t.Setenv("LOOKUP_MODE", mode)
			t.Setenv("LOOKUP_METADATA", string(metadata))
			t.Setenv("COLLECT_DEPS_NO_GO_MOD_DOWNLOAD", "0")
			all := map[string]struct{}{"example.test/dependency": {}}
			licenses := map[string]map[string]struct{}{}
			failures := goLookupResults{}
			enrichGoLicensesFromGoModDownload(all, licenses, []string{dir}, failures)
			rows := buildGoRows(all, licenses, failures)
			count, err := os.ReadFile(filepath.Join(dir, "count"))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "recover":
				if string(count) != "2\n" || rows[0].License != "MIT" {
					t.Fatalf("cold lookup: calls=%s rows=%v", count, rows)
				}
				enrichGoLicensesFromGoModDownload(all, licenses, []string{dir}, failures)
				warm, _ := os.ReadFile(filepath.Join(dir, "count"))
				if string(warm) != string(count) {
					t.Fatal("resolved module was looked up again")
				}
			case "persistent", "permanent":
				want := "3\n"
				if mode == "permanent" {
					want = "1\n"
				}
				if string(count) != want || !strings.Contains(rows[0].License, "lookup unavailable") || !strings.Contains(rows[0].License, "example.test/dependency@v1.2.3") {
					t.Fatalf("calls=%s rows=%v", count, rows)
				}
			case "missing-license":
				if strings.Contains(rows[0].License, "lookup unavailable") || normalizeLicenseGroup(rows[0].License) != "Unresolved" {
					t.Fatalf("rows=%v", rows)
				}
			}
		})
	}
}

// TestGoDownloadPreservesSuccessfulVersion checks that version order cannot
// replace a successful absent-license lookup with an unavailable result.
func TestGoDownloadPreservesSuccessfulVersion(t *testing.T) {
	for _, tt := range []struct {
		name            string
		first           string
		second          string
		wantLicense     string
		wantUnavailable bool
	}{
		{"command failure after success", "missing", "command-error", "", false},
		{"JSON failure after success", "missing", "invalid-json", "", false},
		{"module failure after success", "missing", "module-error", "", false},
		{"success after command failure", "command-error", "missing", "", false},
		{"success after JSON failure", "invalid-json", "missing", "", false},
		{"success after module failure", "module-error", "missing", "", false},
		{"all versions fail", "command-error", "module-error", "", true},
		{"later version has license", "missing", "licensed", "MIT", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeTestFile(t, filepath.Join(dir, "go.sum"), "example.test/dependency v1.0.0 h1:fixture\nexample.test/dependency v1.1.0 h1:fixture\n")
			missingDir := filepath.Join(dir, "missing")
			writeTestFile(t, filepath.Join(missingDir, "go.mod"), "module example.test/dependency\n")
			licensedDir := filepath.Join(dir, "licensed")
			writeTestFile(t, filepath.Join(licensedDir, "LICENSE"), "MIT License\n")
			bin := filepath.Join(dir, "bin")
			writeTestFile(t, filepath.Join(bin, "go"), `#!/bin/sh
printf '%s\n' "$4" >> "$LOOKUP_COUNTER"
case "$4" in
 example.test/dependency@v1.0.0) mode="$LOOKUP_FIRST";;
 example.test/dependency@v1.1.0) mode="$LOOKUP_SECOND";;
 *) exit 2;;
esac
case "$mode" in
 command-error) printf '401 Unauthorized\n' >&2; exit 1;;
 invalid-json) printf '{\n';;
 module-error) printf '{"Error":"unknown revision"}\n';;
 missing) printf '%s\n' "$LOOKUP_MISSING_METADATA";;
 licensed) printf '%s\n' "$LOOKUP_LICENSED_METADATA";;
 *) exit 2;;
esac
`)
			if err := os.Chmod(filepath.Join(bin, "go"), 0755); err != nil {
				t.Fatal(err)
			}
			missingMetadata, err := json.Marshal(map[string]string{"Dir": missingDir})
			if err != nil {
				t.Fatal(err)
			}
			licensedMetadata, err := json.Marshal(map[string]string{"Dir": licensedDir})
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LOOKUP_COUNTER", filepath.Join(dir, "calls"))
			t.Setenv("LOOKUP_FIRST", tt.first)
			t.Setenv("LOOKUP_SECOND", tt.second)
			t.Setenv("LOOKUP_MISSING_METADATA", string(missingMetadata))
			t.Setenv("LOOKUP_LICENSED_METADATA", string(licensedMetadata))
			t.Setenv("COLLECT_DEPS_NO_GO_MOD_DOWNLOAD", "0")
			all := map[string]struct{}{"example.test/dependency": {}}
			licenses := map[string]map[string]struct{}{}
			failures := goLookupResults{"example.test/dependency": {failure: "prior go list failure"}}
			enrichGoLicensesFromGoModDownload(all, licenses, []string{dir}, failures)
			rows := buildGoRows(all, licenses, failures)
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if string(calls) != "example.test/dependency@v1.0.0\nexample.test/dependency@v1.1.0\n" {
				t.Fatalf("unexpected lookup order: %s", calls)
			}
			if got := strings.Contains(rows[0].License, "lookup unavailable"); got != tt.wantUnavailable {
				t.Fatalf("unavailable=%t, want %t: %v", got, tt.wantUnavailable, rows)
			}
			if tt.wantLicense != "" {
				if rows[0].License != tt.wantLicense {
					t.Fatalf("license=%q, want %q", rows[0].License, tt.wantLicense)
				}
			} else if normalizeLicenseGroup(rows[0].License) != "Unresolved" {
				t.Fatalf("missing license must remain unresolved: %v", rows)
			}
		})
	}
}

// TestGoLookupPreservesSuccessAcrossSources checks successful absent-license
// lookups across module roots and the list-to-download fallback boundary.
func TestGoLookupPreservesSuccessAcrossSources(t *testing.T) {
	for _, tt := range []struct {
		name            string
		roots           []string
		download        bool
		wantUnavailable bool
	}{
		{"list success then download failure", []string{"good"}, true, false},
		{"later root failure", []string{"good", "bad"}, false, false},
		{"later root success", []string{"bad", "good"}, false, false},
		{"all sources fail", []string{"bad"}, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			moduleDir := filepath.Join(dir, "module")
			writeTestFile(t, filepath.Join(moduleDir, "go.mod"), "module example.test/dependency\n")
			metadata, err := json.Marshal(map[string]string{"Path": "example.test/dependency", "Dir": moduleDir})
			if err != nil {
				t.Fatal(err)
			}
			var roots []string
			for _, root := range tt.roots {
				path := filepath.Join(dir, root)
				writeTestFile(t, filepath.Join(path, "go.sum"), "example.test/dependency v1.0.0 h1:fixture\nexample.test/other v1.0.0 h1:fixture\n")
				roots = append(roots, path)
			}
			bin := filepath.Join(dir, "bin")
			writeTestFile(t, filepath.Join(bin, "go"), `#!/bin/sh
printf '%s\n' "$*" >> "$LOOKUP_COUNTER"
if [ "$1" = list ]; then
 case "$PWD" in
  */good) printf '%s\n' "$LOOKUP_METADATA"; exit 0;;
 esac
 printf 'unknown revision\n' >&2
 exit 1
fi
printf '401 Unauthorized\n' >&2
exit 1
`)
			if err := os.Chmod(filepath.Join(bin, "go"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LOOKUP_COUNTER", filepath.Join(dir, "calls"))
			t.Setenv("LOOKUP_METADATA", string(metadata))
			t.Setenv("COLLECT_DEPS_NO_GO_LIST", "0")
			t.Setenv("COLLECT_DEPS_NO_GO_MOD_DOWNLOAD", "0")
			all := map[string]struct{}{"example.test/dependency": {}, "example.test/other": {}}
			licenses := map[string]map[string]struct{}{}
			lookups := goLookupResults{}
			enrichGoLicensesFromGoModuleList(all, licenses, roots, lookups)
			if tt.download {
				enrichGoLicensesFromGoModDownload(all, licenses, roots, lookups)
			}
			rows := buildGoRows(all, licenses, lookups)
			if got := strings.Contains(rows[0].License, "lookup unavailable"); got != tt.wantUnavailable {
				t.Fatalf("unavailable=%t, want %t: %v", got, tt.wantUnavailable, rows)
			}
			if !strings.Contains(rows[1].License, "lookup unavailable") {
				t.Fatalf("another module's success must not hide a failure: %v", rows)
			}
			for _, row := range rows {
				if normalizeLicenseGroup(row.License) != "Unresolved" {
					t.Fatalf("missing license must remain unresolved: %v", row)
				}
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "mod download") != tt.download {
				t.Fatalf("download=%t calls=%s", tt.download, calls)
			}
		})
	}
}

// TestGoDownloadUnavailableToolReportsAllModules checks that an unavailable Go
// executable is reported for every unresolved module without losing successes.
func TestGoDownloadUnavailableToolReportsAllModules(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.sum"), "example.test/b-first v1.0.0 h1:fixture\nexample.test/c-second v1.0.0 h1:fixture\n")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("COLLECT_DEPS_NO_GO_LIST", "1")
	t.Setenv("COLLECT_DEPS_NO_GO_MOD_DOWNLOAD", "0")
	all := map[string]struct{}{
		"example.test/a-no-version": {},
		"example.test/b-first":      {},
		"example.test/c-second":     {},
		"example.test/d-inspected":  {},
		"example.test/e-licensed":   {},
	}
	licenses := map[string]map[string]struct{}{}
	addLicense(licenses, "example.test/e-licensed", "MIT")
	lookups := goLookupResults{}
	lookups.recordSuccess("example.test/d-inspected")
	enrichGoLicensesFromGoModuleList(all, licenses, []string{dir}, lookups)
	enrichGoLicensesFromGoModDownload(all, licenses, []string{dir}, lookups)
	for _, row := range buildGoRows(all, licenses, lookups) {
		switch row.SortKey {
		case "example.test/e-licensed":
			if row.License != "MIT" {
				t.Errorf("recognized license changed: %v", row)
			}
		case "example.test/d-inspected":
			if strings.Contains(row.License, "lookup unavailable") || normalizeLicenseGroup(row.License) != "Unresolved" {
				t.Errorf("successful absent-license lookup changed: %v", row)
			}
		default:
			if !strings.Contains(row.License, "lookup unavailable") || !strings.Contains(row.License, "Go executable unavailable; check PATH") {
				t.Errorf("missing unavailable-tool diagnostic: %v", row)
			}
			if normalizeLicenseGroup(row.License) != "Unresolved" {
				t.Errorf("failed lookup must remain unresolved: %v", row)
			}
		}
	}
	addLicense(licenses, "example.test/c-second", "Apache-2.0")
	for _, row := range buildGoRows(all, licenses, lookups) {
		if row.SortKey == "example.test/c-second" && row.License != "Apache-2.0" {
			t.Fatalf("later fallback license ignored: %v", row)
		}
	}
}

// registryTransport redirects only the synthetic npm lookup to a local server.
type registryTransport struct{ endpoint string }

// RoundTrip sends the test request to the local registry fixture.
func (r registryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	local, err := http.NewRequestWithContext(req.Context(), req.Method, r.endpoint, nil)
	if err != nil {
		return nil, err
	}
	copy.URL = local.URL
	return http.DefaultTransport.RoundTrip(copy)
}

// TestNPMLookupFailureAndMissingLicenseRemainDistinct checks npm failure reporting and caching.
func TestNPMLookupFailureAndMissingLicenseRemainDistinct(t *testing.T) {
	for _, mode := range []string{"recover", "persistent", "missing-license"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "persistent" || (mode == "recover" && calls == 1) {
					w.WriteHeader(503)
					return
				}
				if mode == "missing-license" {
					fmt.Fprint(w, `{}`)
					return
				}
				fmt.Fprint(w, `{"license":"MIT"}`)
			}))
			defer server.Close()
			old := http.DefaultClient
			http.DefaultClient = &http.Client{Transport: registryTransport{server.URL}}
			t.Cleanup(func() { http.DefaultClient = old })
			cache := map[string]npmLookupResult{}
			all := map[string]struct{}{"fixture@1.2.3": {}}
			rows, _ := buildNodeRows(all, true, cache)
			firstCalls := calls
			warm, _ := buildNodeRows(all, true, cache)
			if calls != firstCalls || warm[0].License != rows[0].License {
				t.Fatal("warm cache differs")
			}
			switch mode {
			case "recover":
				if rows[0].License != "MIT" || calls != 2 {
					t.Fatalf("rows=%v calls=%d", rows, calls)
				}
			case "persistent":
				if !strings.Contains(rows[0].License, "lookup unavailable") || calls != 3 {
					t.Fatalf("rows=%v calls=%d", rows, calls)
				}
			case "missing-license":
				if !strings.Contains(rows[0].License, "no recognized license") || calls != 1 {
					t.Fatalf("rows=%v calls=%d", rows, calls)
				}
			}
		})
	}
}
