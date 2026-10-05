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
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
	"nvcf-bdd/harness"
)

const maxHTTPFailureBody = 300

// httpPollInterval is the pause between polls; tests shorten it.
var httpPollInterval = 5 * time.Second

// registerHTTPSteps hooks the HTTP request and response assertion steps.
// Request headers are declared with their own steps and apply to the next
// request only, so a header value never appears in the send step text.
func registerHTTPSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^the HTTP request header "([^"]*)" is "([^"]*)"$`, sc.theHTTPRequestHeaderIs)
	ctx.Step(`^the HTTP request header "([^"]*)" is the NVCF CLI admin token with prefix "([^"]*)"$`, sc.theHTTPRequestHeaderIsAdminToken)
	ctx.Step(`^I send an? "([A-Z]+)" request to "([^"]*)"$`, sc.iSendHTTPRequest)
	ctx.Step(`^I send an? "([A-Z]+)" request to "([^"]*)" with body:$`, sc.iSendHTTPRequestWithBody)
	ctx.Step(`^I poll "([A-Z]+)" "([^"]*)" for up to (\d+) seconds until the HTTP response body contains "([^"]*)"$`, sc.iPollHTTPUntilBodyContains)
	ctx.Step(`^the HTTP status should be (\d+)$`, sc.theHTTPStatusShouldBe)
	ctx.Step(`^the HTTP status should be (\d+) or (\d+)$`, sc.theHTTPStatusShouldBeOneOf)
	ctx.Step(`^the HTTP response body should contain "([^"]*)"$`, sc.theHTTPBodyShouldContain)
	ctx.Step(`^the HTTP response body should not contain "([^"]*)"$`, sc.theHTTPBodyShouldNotContain)
	ctx.Step(`^the HTTP response JSON field "([^"]*)" should equal "([^"]*)"$`, sc.theHTTPJSONFieldShouldEqual)
	ctx.Step(`^the HTTP response JSON field "([^"]*)" should be at least (\d+)$`, sc.theHTTPJSONFieldShouldBeAtLeast)
	ctx.Step(`^I export the HTTP response JSON field "([^"]*)" to environment variable "([^"]*)"$`, sc.iExportHTTPJSONField)
}

func (sc *ScenarioContext) httpClient() harness.HTTPClient {
	if sc.Suite.HTTP == nil {
		sc.Suite.HTTP = harness.NewHTTPClient()
	}
	return sc.Suite.HTTP
}

func (sc *ScenarioContext) theHTTPRequestHeaderIs(name, value string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("http header name must be non-empty")
	}
	if sc.HTTPHeaders == nil {
		sc.HTTPHeaders = map[string]string{}
	}
	sc.HTTPHeaders[name] = dsl.Interpolate(value)
	return nil
}

// theHTTPRequestHeaderIsAdminToken reads the admin token from the state file
// of the selected NVCF CLI config so the JWT never enters argv, env or logs.
func (sc *ScenarioContext) theHTTPRequestHeaderIsAdminToken(name, prefix string) error {
	if sc.NVCFCLIConfig == "" {
		return fmt.Errorf("no NVCF CLI config selected; use 'I use NVCF CLI config' first")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	token, err := dsl.CLIStateToken(dsl.CLIStatePath(home, sc.NVCFCLIConfig))
	if err != nil {
		return err
	}
	if sc.HTTPHeaders == nil {
		sc.HTTPHeaders = map[string]string{}
	}
	sc.HTTPHeaders[name] = prefix + token
	return nil
}

func (sc *ScenarioContext) iSendHTTPRequest(ctx context.Context, method, url string) error {
	return sc.sendHTTP(ctx, method, url, "")
}

func (sc *ScenarioContext) iSendHTTPRequestWithBody(ctx context.Context, method, url string, doc *godog.DocString) error {
	return sc.sendHTTP(ctx, method, url, doc.Content)
}

// sendHTTP interpolates the URL and body, sends one request with the pending
// headers, and records the response. A non-2xx status is not a step failure;
// the status assertion decides.
func (sc *ScenarioContext) sendHTTP(ctx context.Context, method, url, body string) error {
	headers := sc.HTTPHeaders
	sc.HTTPHeaders = nil
	resp, err := sc.httpClient().Do(ctx, method, dsl.Interpolate(url), headers, dsl.Interpolate(body))
	if err != nil {
		sc.LastHTTP = nil
		return err
	}
	sc.LastHTTP = &resp
	return nil
}

func (sc *ScenarioContext) iPollHTTPUntilBodyContains(ctx context.Context, method, url string, seconds int, text string) error {
	want := dsl.Interpolate(text)
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf("poll text must be non-empty")
	}
	headers := sc.HTTPHeaders
	sc.HTTPHeaders = nil
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for {
		resp, err := sc.httpClient().Do(ctx, method, dsl.Interpolate(url), headers, "")
		if err == nil {
			sc.LastHTTP = &resp
			if strings.Contains(resp.Body, want) {
				return nil
			}
		}
		if time.Now().Add(httpPollInterval).After(deadline) {
			if err != nil {
				return fmt.Errorf("poll timed out after %d seconds: %w", seconds, err)
			}
			return fmt.Errorf("poll timed out after %d seconds: last status %d, body missing %q: %s",
				seconds, sc.LastHTTP.Status, want, truncateBody(sc.LastHTTP.Body))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(httpPollInterval):
		}
	}
}

func (sc *ScenarioContext) requireHTTP() (harness.HTTPResponse, error) {
	if sc.LastHTTP == nil {
		return harness.HTTPResponse{}, fmt.Errorf("no HTTP response recorded; send a request first")
	}
	return *sc.LastHTTP, nil
}

func (sc *ScenarioContext) theHTTPStatusShouldBe(want int) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	if resp.Status != want {
		return fmt.Errorf("http status = %d, want %d: %s", resp.Status, want, truncateBody(resp.Body))
	}
	return nil
}

func (sc *ScenarioContext) theHTTPStatusShouldBeOneOf(first, second int) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	if resp.Status != first && resp.Status != second {
		return fmt.Errorf("http status = %d, want %d or %d: %s", resp.Status, first, second, truncateBody(resp.Body))
	}
	return nil
}

func (sc *ScenarioContext) theHTTPBodyShouldContain(text string) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	want := dsl.Interpolate(text)
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf("expected text must be non-empty")
	}
	if !strings.Contains(resp.Body, want) {
		return fmt.Errorf("http response body missing %q: %s", want, truncateBody(resp.Body))
	}
	return nil
}

func (sc *ScenarioContext) theHTTPBodyShouldNotContain(text string) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	unwanted := dsl.Interpolate(text)
	if strings.TrimSpace(unwanted) == "" {
		return fmt.Errorf("unwanted text must be non-empty")
	}
	if strings.Contains(resp.Body, unwanted) {
		return fmt.Errorf("http response body unexpectedly contains %q", unwanted)
	}
	return nil
}

func (sc *ScenarioContext) theHTTPJSONFieldShouldEqual(path, want string) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	got, err := dsl.JSONField(resp.Body, path)
	if err != nil {
		return err
	}
	if expected := dsl.Interpolate(want); got != expected {
		return fmt.Errorf("json field %q = %q, want %q", path, got, expected)
	}
	return nil
}

func (sc *ScenarioContext) theHTTPJSONFieldShouldBeAtLeast(path string, min int) error {
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	return dsl.JSONFieldAtLeast(resp.Body, path, float64(min))
}

// iExportHTTPJSONField stores a response field in an env var through the
// EnvLedger, which restores the prior value at suite teardown. The value is
// never echoed.
func (sc *ScenarioContext) iExportHTTPJSONField(path, name string) error {
	if name == "" {
		return fmt.Errorf("export: env var name must be non-empty")
	}
	resp, err := sc.requireHTTP()
	if err != nil {
		return err
	}
	value, err := dsl.JSONField(resp.Body, path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("export to %s: json field %q is empty", name, path)
	}
	if err := sc.Suite.EnvLedger.Snapshot(name); err != nil {
		return fmt.Errorf("export to %s: snapshot: %w", name, err)
	}
	if err := os.Setenv(name, value); err != nil {
		return fmt.Errorf("export to %s: setenv: %w", name, err)
	}
	return nil
}

func truncateBody(body string) string {
	if len(body) <= maxHTTPFailureBody {
		return body
	}
	return body[:maxHTTPFailureBody] + "..." + strconv.Itoa(len(body)) + " bytes"
}
