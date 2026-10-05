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

package steps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nvcf-bdd/harness"
)

type fakeHTTP struct {
	responses []harness.HTTPResponse
	err       error
	calls     int
	method    string
	url       string
	headers   map[string]string
	body      string
}

func (f *fakeHTTP) Do(_ context.Context, method, url string, headers map[string]string, body string) (harness.HTTPResponse, error) {
	f.method, f.url, f.headers, f.body = method, url, headers, body
	i := f.calls
	f.calls++
	if f.err != nil {
		return harness.HTTPResponse{}, f.err
	}
	if i >= len(f.responses) {
		i = len(f.responses) - 1
	}
	return f.responses[i], nil
}

func newHTTPScenario(t *testing.T, responses ...harness.HTTPResponse) (*ScenarioContext, *fakeHTTP) {
	t.Helper()
	sc, _ := newScenarioContext(t)
	fake := &fakeHTTP{responses: responses}
	sc.Suite.HTTP = fake
	return sc, fake
}

func TestSendHTTPAppliesPendingHeadersOnceAndInterpolates(t *testing.T) {
	t.Setenv("BDD_HTTP_HOST", "events.localhost:8080")
	sc, fake := newHTTPScenario(t, harness.HTTPResponse{Status: 200, Body: "{}"})
	if err := sc.theHTTPRequestHeaderIs("Authorization", "Bearer ${BDD_HTTP_HOST}"); err != nil {
		t.Fatal(err)
	}
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://${BDD_HTTP_HOST}/health"); err != nil {
		t.Fatal(err)
	}
	if fake.url != "http://events.localhost:8080/health" || fake.headers["Authorization"] != "Bearer events.localhost:8080" {
		t.Fatalf("url=%q headers=%v", fake.url, fake.headers)
	}
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if len(fake.headers) != 0 {
		t.Fatalf("headers leaked to next request: %v", fake.headers)
	}
}

func TestHTTPStatusAssertion(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 401, Body: "nope"})
	if err := sc.theHTTPStatusShouldBe(401); err == nil {
		t.Fatal("assertion must fail before any request")
	}
	if err := sc.iSendHTTPRequest(context.Background(), "POST", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPStatusShouldBe(401); err != nil {
		t.Fatalf("401: %v", err)
	}
	if err := sc.theHTTPStatusShouldBe(200); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected status mismatch with body, got %v", err)
	}
}

func TestHTTPStatusOneOfAssertion(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 403, Body: "forbidden"})
	if err := sc.iSendHTTPRequest(context.Background(), "POST", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPStatusShouldBeOneOf(401, 403); err != nil {
		t.Fatalf("403 should satisfy 401 or 403: %v", err)
	}
	if err := sc.theHTTPStatusShouldBeOneOf(200, 201); err == nil {
		t.Fatal("expected mismatch")
	}
}

func TestHTTPSendErrorClearsLastResponse(t *testing.T) {
	sc, fake := newHTTPScenario(t, harness.HTTPResponse{Status: 200})
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://x/"); err != nil {
		t.Fatal(err)
	}
	fake.err = errors.New("connection refused")
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://x/"); err == nil {
		t.Fatal("expected send error")
	}
	if err := sc.theHTTPStatusShouldBe(200); err == nil {
		t.Fatal("stale response must not satisfy assertion")
	}
}

func TestHTTPBodyAndJSONAssertions(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 200, Body: `{"summary":{"by_event":{"InstanceReady":2}},"events":[{"source":"nvidia-spot"}]}`})
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPBodyShouldContain("InstanceReady"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPBodyShouldNotContain("InstanceDestroyed"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPBodyShouldNotContain("InstanceReady"); err == nil {
		t.Fatal("not-contain must fail when present")
	}
	if err := sc.theHTTPBodyShouldContain(" "); err == nil {
		t.Fatal("blank text must be rejected")
	}
	if err := sc.theHTTPJSONFieldShouldEqual("events.0.source", "nvidia-spot"); err != nil {
		t.Fatal(err)
	}
	if err := sc.theHTTPJSONFieldShouldEqual("events.0.source", "nvca"); err == nil {
		t.Fatal("expected mismatch")
	}
	if err := sc.theHTTPJSONFieldShouldBeAtLeast("summary.by_event.InstanceReady", 1); err != nil {
		t.Fatal(err)
	}
}

func TestExportHTTPJSONFieldSetsEnvAndRestores(t *testing.T) {
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 200, Body: `{"value":"nvapi-abc"}`})
	t.Setenv("BDD_EXPORT_KEY", "before")
	if err := sc.iSendHTTPRequest(context.Background(), "POST", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if err := sc.iExportHTTPJSONField("value", "BDD_EXPORT_KEY"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("BDD_EXPORT_KEY"); got != "nvapi-abc" {
		t.Fatalf("env = %q", got)
	}
	if err := sc.Suite.EnvLedger.RestoreAll(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("BDD_EXPORT_KEY"); got != "before" {
		t.Fatalf("env after restore = %q", got)
	}
	if err := sc.iExportHTTPJSONField("missing", "BDD_EXPORT_KEY"); err == nil {
		t.Fatal("missing field must fail")
	}
}

func TestPollHTTPRetriesUntilBodyContains(t *testing.T) {
	old := httpPollInterval
	httpPollInterval = time.Millisecond
	t.Cleanup(func() { httpPollInterval = old })
	sc, fake := newHTTPScenario(t,
		harness.HTTPResponse{Status: 200, Body: `{"events":[]}`},
		harness.HTTPResponse{Status: 200, Body: `{"events":[{"event_name":"InstanceReady"}]}`},
	)
	if err := sc.iPollHTTPUntilBodyContains(context.Background(), "GET", "http://x/", 5, "InstanceReady"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 {
		t.Fatalf("calls = %d, want 2", fake.calls)
	}
}

func TestPollHTTPTimesOut(t *testing.T) {
	old := httpPollInterval
	httpPollInterval = 400 * time.Millisecond
	t.Cleanup(func() { httpPollInterval = old })
	sc, _ := newHTTPScenario(t, harness.HTTPResponse{Status: 200, Body: `{"events":[]}`})
	err := sc.iPollHTTPUntilBodyContains(context.Background(), "GET", "http://x/", 1, "InstanceReady")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestAdminTokenHeaderReadsStateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".nvcf-cli.nvcf-cli-local.state"), []byte(`{"token":"jwt-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, fake := newHTTPScenario(t, harness.HTTPResponse{Status: 200})
	if err := sc.theHTTPRequestHeaderIsAdminToken("Authorization", "Bearer "); err == nil {
		t.Fatal("must require a selected CLI config")
	}
	sc.NVCFCLIConfig = "/repo/tests/bdd/fixtures/nvcf-cli-local.yaml"
	if err := sc.theHTTPRequestHeaderIsAdminToken("Authorization", "Bearer "); err != nil {
		t.Fatal(err)
	}
	if err := sc.iSendHTTPRequest(context.Background(), "GET", "http://x/"); err != nil {
		t.Fatal(err)
	}
	if fake.headers["Authorization"] != "Bearer jwt-1" {
		t.Fatalf("headers = %v", fake.headers)
	}
}
