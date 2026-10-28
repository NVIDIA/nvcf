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

package bdd_tmp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"nvcf-bdd/harness"
	"nvcf-bdd/steps"
)

const (
	decisionLLMHost    = "llm.localhost:8080"
	decisionVanityHost = "vanity-openai.localhost:8080"
	decisionsRoute     = "/v1/decisions"
	systemOneRoute     = "/v1/systemone"
)

// decisionRoutesFake stands in for the LLM gateway, the Vanity Gateway and
// the sample backend on the decision routes. A model serves a route only when
// the scenario's configuration lists it there. Each broken mode reproduces one
// gateway failure the scenarios must reject.
type decisionRoutesFake struct {
	broken string
	seen   map[string]bool
	inner  harness.HTTPClient
}

func newDecisionRoutesFake(broken string, inner harness.HTTPClient) *decisionRoutesFake {
	return &decisionRoutesFake{broken: broken, seen: map[string]bool{}, inner: inner}
}

// decisionRoutesByModel lists the routes each model name serves, by host.
var decisionRoutesByModel = map[string]map[string][]string{
	decisionLLMHost: {
		"function-1/bdd-decision-both": {decisionsRoute, systemOneRoute},
		"function-1/bdd-decision-only": {decisionsRoute},
	},
	decisionVanityHost: {
		"bdd-decision-direct":         {decisionsRoute, systemOneRoute},
		"bdd-decision-llm":            {decisionsRoute, systemOneRoute},
		"bdd-decision-decisions-only": {decisionsRoute},
		"bdd-decision-systemone-only": {systemOneRoute},
	},
}

func isDecisionRoutePath(path string) bool {
	return path == decisionsRoute || path == systemOneRoute || path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")
}

func (f *decisionRoutesFake) mark(mode string) bool {
	if f.broken != mode {
		return false
	}
	f.seen[mode] = true
	return true
}

func (f *decisionRoutesFake) Do(ctx context.Context, method, rawURL string, headers map[string]string, body string) (harness.HTTPResponse, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return harness.HTTPResponse{}, err
	}
	known := decisionRoutesByModel[u.Host]
	if known == nil || !isDecisionRoutePath(u.Path) {
		return f.inner.Do(ctx, method, rawURL, headers, body)
	}
	if headers["Authorization"] != "Bearer wiring-function-api-key" {
		return harness.HTTPResponse{Status: 401}, nil
	}
	if strings.HasPrefix(u.Path, "/v1/models") {
		return f.models(u, method, known)
	}
	if method != "POST" || headers["Content-Type"] != "application/json" {
		return harness.HTTPResponse{Status: 400}, nil
	}
	var payload struct {
		Model     string          `json:"model"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return harness.HTTPResponse{}, err
	}
	if !f.serves(u, payload.Model, known) {
		return f.notFound(u, payload.Model), nil
	}
	return f.backend(u, headers, payload.Questions), nil
}

// serves reports whether the model is enabled for the called route. The
// systemone-not-isolated and vanity-section-leak modes enable a model that the
// configuration lists only on the other route.
func (f *decisionRoutesFake) serves(u *url.URL, model string, known map[string][]string) bool {
	for _, route := range known[model] {
		if route == u.Path {
			return true
		}
	}
	switch {
	case u.Host == decisionLLMHost && model == "function-1/bdd-decision-only" && u.Path == systemOneRoute:
		return f.mark("systemone-not-isolated")
	case u.Host == decisionVanityHost && model == "bdd-decision-decisions-only" && u.Path == systemOneRoute:
		return f.mark("vanity-section-leak")
	}
	return false
}

func (f *decisionRoutesFake) notFound(u *url.URL, model string) harness.HTTPResponse {
	message := "model " + model + " is not enabled for " + u.Path
	if u.Path == decisionsRoute || f.mark("systemone-404-openai-shape") {
		return harness.HTTPResponse{Status: 404, Body: mustJSON(map[string]any{
			"error": map[string]any{"message": message, "type": "invalid_request_error"},
		})}
	}
	topLevelID := "req-404"
	if f.mark("request-id-mismatch") {
		topLevelID = "req-other"
	}
	return harness.HTTPResponse{Status: 404, Body: mustJSON(map[string]any{
		"error":      map[string]any{"code": "not_found", "message": message, "request_id": "req-404"},
		"request_id": topLevelID,
	})}
}

// backend answers the way the sample does: canned answers, a header echo and
// the mock_error controls. The gateway under test passes all of it through.
func (f *decisionRoutesFake) backend(u *url.URL, headers map[string]string, questions json.RawMessage) harness.HTTPResponse {
	systemOne := u.Path == systemOneRoute
	switch u.Query().Get("mock_error") {
	case "429":
		return f.mockError(systemOne, http.StatusTooManyRequests, "mock rate limit", "rate_limit_error")
	case "400":
		return f.mockError(systemOne, http.StatusBadRequest, "mock validation error", "invalid_request_error")
	}
	echo := map[string]any{}
	for _, name := range []string{"typesafe-danger-zone-images", "x-vendor-test"} {
		for header, value := range headers {
			if !strings.EqualFold(header, name) {
				continue
			}
			if name == "x-vendor-test" && f.mark("vendor-header-dropped") {
				continue
			}
			echo[name] = value
		}
	}
	response := harness.HTTPResponse{Status: 200, Header: http.Header{}}
	if value, ok := echo["typesafe-danger-zone-images"]; ok {
		response.Header.Set("x-mock-echo-typesafe-danger-zone-images", value.(string))
	}
	if systemOne {
		response.Body = mustJSON(map[string]any{"model": "m", "answers": systemOneAnswers(questions), "echo": echo})
		return response
	}
	answers := decisionAnswers(questions)
	if f.mark("answers-reordered") {
		for i, j := 0, len(answers)-1; i < j; i, j = i+1, j-1 {
			answers[i], answers[j] = answers[j], answers[i]
		}
	}
	response.Body = mustJSON(map[string]any{"model": "m", "answers": answers, "echo": echo})
	return response
}

func (f *decisionRoutesFake) mockError(systemOne bool, status int, message, errorType string) harness.HTTPResponse {
	response := harness.HTTPResponse{Status: status, Header: http.Header{}}
	if status == http.StatusTooManyRequests && !f.mark("retry-after-lost") {
		response.Header.Set("Retry-After", "7")
	}
	switch {
	case f.mark("error-body-rewritten"):
		response.Body = mustJSON(map[string]any{"error": map[string]any{"message": "upstream error", "type": "server_error"}})
	case systemOne:
		response.Body = mustJSON(map[string]any{"detail": []any{map[string]any{"loc": []string{"body"}, "msg": message, "type": "value_error"}}})
	default:
		response.Body = mustJSON(map[string]any{"error": map[string]any{"message": message, "type": errorType, "code": http.StatusText(status)}})
	}
	return response
}

func (f *decisionRoutesFake) models(u *url.URL, method string, known map[string][]string) (harness.HTTPResponse, error) {
	if method != "GET" {
		return harness.HTTPResponse{Status: 405}, nil
	}
	if u.Path == "/v1/models" {
		data := []any{}
		for _, id := range []string{"bdd-decision-direct", "bdd-decision-llm", "bdd-decision-decisions-only", "bdd-decision-systemone-only"} {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		return harness.HTTPResponse{Status: 200, Body: mustJSON(map[string]any{"object": "list", "data": data})}, nil
	}
	id := strings.TrimPrefix(u.Path, "/v1/models/")
	routes, ok := known[id]
	if !ok {
		return harness.HTTPResponse{Status: 404}, nil
	}
	info := map[string]any{"id": id, "object": "model"}
	if !f.mark("models-without-endpoints") {
		info["supported_endpoints"] = routes
	}
	return harness.HTTPResponse{Status: 200, Body: mustJSON(info)}, nil
}

// attach answers the scenarios' warm-up invocations on the runner. They go
// through the NVCF CLI, which prints the function's reply.
func (f *decisionRoutesFake) attach(runner *fakeRunner) {
	previous := runner.onRun
	runner.onRun = func(command string) error {
		if strings.Contains(command, " function invoke ") && strings.Contains(command, "-ready") &&
			(strings.Contains(command, "--model-name bdd-decision") || strings.Contains(command, "bdd-decision-direct-ready")) {
			runner.results[command] = harness.Result{Stdout: "Function invocation completed!\n\nResponse:\n" +
				`{"answers":[{"type":"predicate","probability":0.99}]}` + "\n"}
		}
		if previous != nil {
			return previous(command)
		}
		return nil
	}
}

func decisionAnswers(questions json.RawMessage) []any {
	var list []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Choices []any  `json:"choices"`
	}
	_ = json.Unmarshal(questions, &list)
	answers := []any{}
	for _, question := range list {
		answer := map[string]any{"type": question.Type, "name": question.Name}
		switch question.Type {
		case "predicate":
			answer["probability"] = 0.99
		case "choice":
			answer["choice"] = question.Choices[0]
		case "score":
			answer["score"] = 0.3
		}
		answers = append(answers, answer)
	}
	return answers
}

func systemOneAnswers(questions json.RawMessage) map[string]any {
	var byName map[string]struct {
		Type    string `json:"type"`
		Choices []any  `json:"choices"`
	}
	_ = json.Unmarshal(questions, &byName)
	answers := map[string]any{}
	for name, question := range byName {
		answer := map[string]any{"type": question.Type}
		switch question.Type {
		case "noul":
			answer["noul"] = 0.99
		case "choice":
			answer["choice"] = question.Choices[0]
		case "score":
			answer["score"] = 0.3
		}
		answers[name] = answer
	}
	return answers
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// decisionScenarios returns the two decision route scenarios from the
// single-cluster feature, without the scenarios around them.
func decisionScenarios(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(mustResolveFeaturePath(t, "single-cluster-helmfile.feature"))
	if err != nil {
		t.Fatal(err)
	}
	_, scenarios, found := strings.Cut(string(data), "\n    # Beta decision routes")
	if !found {
		t.Fatal("missing decision route scenarios")
	}
	return "    # Beta decision routes" + scenarios
}

func TestDecisionRouteScenariosRejectBrokenGateways(t *testing.T) {
	scenarios := decisionScenarios(t)
	for _, tag := range []string{"@llm-function-type @llm-decision-routes\n", "@vanity-decision-routes\n"} {
		if !strings.Contains(scenarios, tag) {
			t.Fatalf("decision scenarios lost tag line %q", tag)
		}
	}
	brokenModes := []string{
		"", "answers-reordered", "vendor-header-dropped", "systemone-not-isolated",
		"systemone-404-openai-shape", "request-id-mismatch", "retry-after-lost",
		"error-body-rewritten", "vanity-section-leak", "models-without-endpoints",
	}
	for _, broken := range brokenModes {
		t.Run(broken, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("REPO_ROOT", "/repo-root-placeholder")
			t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
			t.Setenv("SAMPLE_LLM_DECISIONS_IMAGE", "nvcr.io/test-org/test-team/openai-compatible-decisions:test")
			seedFunctionAPIKey(t, home)
			runner := newFakeRunner(map[string]harness.Result{})
			runner.onRun = func(command string) error {
				if strings.Contains(command, " status --json") {
					runner.results[command] = harness.Result{Stdout: `{"currentFunction":{"hasFunction":true,"functionId":"function-1","versionId":"version-1"}}`}
				}
				return nil
			}
			suite := newWiringSuite(t, runner)
			envFile := filepath.Join(suite.Config.RepoRoot, "deploy", "stacks", "self-managed", "environments", "local-bdd.yaml")
			if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(envFile, []byte("addons: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			fake := newDecisionRoutesFake(broken, &llmRoutingHTTPFake{seen: map[string]bool{}})
			newVanityShadowFake("", fake).attach(runner)
			fake.attach(runner)
			suite.HTTP = fake
			sc := steps.NewScenarioContext(suite)
			var out strings.Builder
			status := godog.TestSuite{
				Name:                "decision-routes-feature",
				ScenarioInitializer: func(ctx *godog.ScenarioContext) { steps.RegisterAll(ctx, sc) },
				Options: &godog.Options{
					Format: "pretty", Output: &out, Strict: true,
					FeatureContents: []godog.Feature{{Name: "decisions", Contents: []byte("Feature: Decision routes\n" + scenarios)}},
				},
			}.Run()
			if broken == "" {
				if status != 0 {
					t.Fatalf("feature failed:\n%s", out.String())
				}
				if !commandRanThatContainsAll(runner.runs, "function create --name bdd-llm-decision-routes",
					"--image nvcr.io/test-org/test-team/openai-compatible-decisions:test", "--function-type LLM",
					"name=bdd-decision-only,uris=/v1/decisions") {
					t.Error("LLM gateway function was not created from SAMPLE_LLM_DECISIONS_IMAGE with both models")
				}
				if !commandRanThatContainsAll(runner.runs, "function create --name bdd-vanity-decision-direct",
					"--image nvcr.io/test-org/test-team/openai-compatible-decisions:test", "--inference-url /v1/decisions") {
					t.Error("direct Vanity function was not created from SAMPLE_LLM_DECISIONS_IMAGE")
				}
				return
			}
			if !fake.seen[broken] || status == 0 {
				t.Fatalf("feature did not reject broken %s gateway (exercised %v):\n%s", broken, fake.seen[broken], out.String())
			}
		})
	}
}
