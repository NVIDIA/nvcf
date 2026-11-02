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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

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
// the configuration the scenario wrote lists it there: the --llm-model options
// of the function it created for the LLM gateway, and the Vanity Gateway
// mapping it set in the Helmfile environment. Each broken mode reproduces one
// gateway failure the scenarios must reject.
type decisionRoutesFake struct {
	broken   string
	seen     map[string]bool
	inner    harness.HTTPClient
	runner   *fakeRunner
	repoRoot string
}

func newDecisionRoutesFake(broken string, inner harness.HTTPClient) *decisionRoutesFake {
	return &decisionRoutesFake{broken: broken, seen: map[string]bool{}, inner: inner}
}

// decisionVanityEnvironment is the Helmfile environment the Vanity scenario
// writes its mapping into, relative to the repository root.
var decisionVanityEnvironment = filepath.Join("deploy", "stacks", "self-managed", "environments", "local-bdd.yaml")

var llmModelOption = regexp.MustCompile(`--llm-model '?name=([^,' ]+),uris=([^,' ]+)`)

// routesByModel lists the routes each model name serves on the host. LLM
// gateway models are keyed by name without the function ID prefix.
func (f *decisionRoutesFake) routesByModel(host string) map[string][]string {
	switch host {
	case decisionLLMHost:
		return f.llmRoutes()
	case decisionVanityHost:
		return f.vanityRoutes()
	}
	return nil
}

func (f *decisionRoutesFake) llmRoutes() map[string][]string {
	routes := map[string][]string{}
	if f.runner == nil {
		return routes
	}
	for _, command := range f.runner.runs {
		if !strings.Contains(command, " function create ") {
			continue
		}
		for _, match := range llmModelOption.FindAllStringSubmatch(command, -1) {
			for _, uri := range strings.Split(match[2], "|") {
				if uri == decisionsRoute || uri == systemOneRoute {
					routes[match[1]] = append(routes[match[1]], uri)
				}
			}
		}
	}
	return routes
}

func (f *decisionRoutesFake) vanityRoutes() map[string][]string {
	routes := map[string][]string{}
	data, err := os.ReadFile(filepath.Join(f.repoRoot, decisionVanityEnvironment))
	if err != nil {
		return routes
	}
	var environment struct {
		Addons struct {
			VanityGateway struct {
				MappingConfig struct {
					V2Config struct {
						OpenAI struct {
							Decisions map[string]struct {
								ModelName string `yaml:"modelName"`
							} `yaml:"decisions"`
						} `yaml:"openai"`
						TypesafeSystemOne map[string]struct {
							ModelName string `yaml:"modelName"`
						} `yaml:"typesafeSystemOne"`
					} `yaml:"v2config"`
				} `yaml:"mappingConfig"`
			} `yaml:"vanityGateway"`
		} `yaml:"addons"`
	}
	if yaml.Unmarshal(data, &environment) != nil {
		return routes
	}
	v2 := environment.Addons.VanityGateway.MappingConfig.V2Config
	for _, entry := range v2.OpenAI.Decisions {
		routes[entry.ModelName] = append(routes[entry.ModelName], decisionsRoute)
	}
	for _, entry := range v2.TypesafeSystemOne {
		routes[entry.ModelName] = append(routes[entry.ModelName], systemOneRoute)
	}
	return routes
}

// modelKey strips the function ID prefix the LLM gateway requires. A model
// without it is unknown there.
func modelKey(host, model string) string {
	if host != decisionLLMHost {
		return model
	}
	_, name, found := strings.Cut(model, "/")
	if !found {
		return ""
	}
	return name
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
	if (u.Host != decisionLLMHost && u.Host != decisionVanityHost) || !isDecisionRoutePath(u.Path) {
		return f.inner.Do(ctx, method, rawURL, headers, body)
	}
	if headers["Authorization"] != "Bearer wiring-function-api-key" {
		return f.gatewayError(u, http.StatusUnauthorized, "invalid_api_key", "authentication_error", "authentication failed", "401-not-native"), nil
	}
	known := f.routesByModel(u.Host)
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
	if u.Host == decisionLLMHost && f.rateLimited(modelKey(u.Host, payload.Model)) && !f.mark("rate-limit-not-enforced") {
		return f.gatewayError(u, http.StatusTooManyRequests, "rate_limit_exceeded", "rate_limit_error", "token rate limit exceeded", "429-not-native"), nil
	}
	if strings.Contains(body, "data:image/") && f.mark("image-body-rejected") {
		return harness.HTTPResponse{Status: 413, Body: mustJSON(map[string]any{
			"error": map[string]any{"message": "request too large", "type": "invalid_request_error", "code": "request_too_large"},
		})}, nil
	}
	return f.backend(u, headers, payload.Questions, body), nil
}

// gatewayError is an error the gateway generates, in the native shape of the
// route. The broken mode replaces it with a body in neither shape.
func (f *decisionRoutesFake) gatewayError(u *url.URL, status int, code, errorType, message, broken string) harness.HTTPResponse {
	if f.mark(broken) {
		return harness.HTTPResponse{Status: status, Body: mustJSON(map[string]any{"message": message})}
	}
	if u.Path == systemOneRoute {
		return harness.HTTPResponse{Status: status, Body: mustJSON(map[string]any{
			"error":      map[string]any{"code": code, "message": message, "request_id": "req-gw"},
			"request_id": "req-gw",
		})}
	}
	return harness.HTTPResponse{Status: status, Body: mustJSON(map[string]any{
		"error": map[string]any{"message": message, "type": errorType, "param": nil, "code": code},
	})}
}

var tokenRateLimitOption = regexp.MustCompile(`--llm-model '?name=([^,' ]+),[^' ]*tokenRateLimit=([^,' ]+)`)

// rateLimited reports whether a created function declares a token rate limit
// for the LLM gateway model. The scenario's limit is below any request.
func (f *decisionRoutesFake) rateLimited(model string) bool {
	if f.runner == nil {
		return false
	}
	for _, command := range f.runner.runs {
		for _, match := range tokenRateLimitOption.FindAllStringSubmatch(command, -1) {
			if match[1] == model && strings.Contains(command, " function create ") {
				return true
			}
		}
	}
	return false
}

// inputTokenEstimate mirrors the gateway estimate closely enough for the
// scenarios: about four bytes per text token, and a flat 1024 per inline
// image whose base64 data is not counted.
func (f *decisionRoutesFake) inputTokenEstimate(body string) int {
	images := strings.Count(body, `"type":"input_image"`)
	if images > 0 && f.mark("image-metered-as-one") {
		return 1
	}
	text := regexp.MustCompile(`data:image/[^;]+;base64,[A-Za-z0-9+/=]*`).ReplaceAllString(body, "")
	return max(1, len(text)/4+images*1024)
}

// serves reports whether the model is enabled for the called route. The
// systemone-not-isolated and vanity-section-leak modes enable a model that the
// configuration lists only on the other route.
func (f *decisionRoutesFake) serves(u *url.URL, model string, known map[string][]string) bool {
	for _, route := range known[modelKey(u.Host, model)] {
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
func (f *decisionRoutesFake) backend(u *url.URL, headers map[string]string, questions json.RawMessage, body string) harness.HTTPResponse {
	systemOne := u.Path == systemOneRoute
	var response harness.HTTPResponse
	switch u.Query().Get("mock_error") {
	case "429":
		response = f.mockError(systemOne, http.StatusTooManyRequests, "mock rate limit", "rate_limit_error")
	case "400":
		response = f.mockError(systemOne, http.StatusBadRequest, "mock validation error", "invalid_request_error")
	default:
		return f.success(u, headers, questions, body)
	}
	if u.Host == decisionVanityHost && response.Status == http.StatusTooManyRequests && f.mark("vanity-retry-after-lost") {
		response.Header.Del("Retry-After")
	}
	if u.Host == decisionVanityHost && f.mark("vanity-error-rewritten") {
		response.Body = mustJSON(map[string]any{"error": map[string]any{"message": "upstream error", "type": "server_error"}})
	}
	return response
}

func (f *decisionRoutesFake) success(u *url.URL, headers map[string]string, questions json.RawMessage, body string) harness.HTTPResponse {
	systemOne := u.Path == systemOneRoute
	// The gateway strips caller credentials and sets X-Input-Tokens.
	echo := map[string]any{"credentials_received": f.mark("credentials-forwarded")}
	if !f.mark("input-tokens-dropped") {
		echo["x-input-tokens"] = strconv.Itoa(f.inputTokenEstimate(body))
	}
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
		ids := make([]string, 0, len(known))
		for id := range known {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		data := []any{}
		for _, id := range ids {
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
// through the NVCF CLI, which prints the function's reply. The fake reads the
// created functions from the runner and the Vanity mapping under repoRoot.
func (f *decisionRoutesFake) attach(runner *fakeRunner, repoRoot string) {
	f.runner = runner
	f.repoRoot = repoRoot
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
		"input-tokens-dropped", "credentials-forwarded", "image-body-rejected",
		"vanity-retry-after-lost", "vanity-error-rewritten",
		"401-not-native", "429-not-native", "rate-limit-not-enforced", "image-metered-as-one",
	}
	for _, broken := range brokenModes {
		t.Run(broken, func(t *testing.T) {
			status, out, fake, runner := runDecisionScenarios(t, broken, scenarios)
			if broken == "" {
				if status != 0 {
					t.Fatalf("feature failed:\n%s", out)
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
				t.Fatalf("feature did not reject broken %s gateway (exercised %v):\n%s", broken, fake.seen[broken], out)
			}
		})
	}
}

// runDecisionScenarios runs the decision scenarios against the fake with the
// given broken mode and returns the godog status and output.
func runDecisionScenarios(t *testing.T, broken, scenarios string) (int, string, *decisionRoutesFake, *fakeRunner) {
	t.Helper()
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
	envFile := filepath.Join(suite.Config.RepoRoot, decisionVanityEnvironment)
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("addons: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newDecisionRoutesFake(broken, &llmRoutingHTTPFake{seen: map[string]bool{}})
	newVanityShadowFake("", fake).attach(runner)
	fake.attach(runner, suite.Config.RepoRoot)
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
	return status, out.String(), fake, runner
}

// TestDecisionRouteScenariosFollowTheirOwnConfiguration breaks the model
// configuration the scenarios write. The fake serves only what that
// configuration declares, so each typo must fail the feature offline.
func TestDecisionRouteScenariosFollowTheirOwnConfiguration(t *testing.T) {
	scenarios := decisionScenarios(t)
	for name, edit := range map[string][2]string{
		"vanity decisions section key": {"\n        decisions:\n", "\n        decision:\n"},
		"vanity systemone section key": {`key "addons.vanityGateway.mappingConfig.v2config.typesafeSystemOne"`, `key "addons.vanityGateway.mappingConfig.v2config.typesafeSystemone"`},
		"vanity model name":            {"modelName: bdd-decision-direct\n", "modelName: bdd-decision-direkt\n"},
		"llm model uris":               {"name=bdd-decision-both,uris=/v1/decisions\\|/v1/systemone", "name=bdd-decision-both,uris=/v1/decisions"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(scenarios, edit[0]) {
				t.Fatalf("scenario text %q not found", edit[0])
			}
			broken := strings.Replace(scenarios, edit[0], edit[1], 1)
			status, out, _, _ := runDecisionScenarios(t, "", broken)
			if status == 0 {
				t.Fatalf("feature passed with a broken configuration (%s):\n%s", name, out)
			}
		})
	}
}

// TestDecisionRouteScenariosSkipWithoutDecisionsImage runs the decision
// scenarios with the live tag filter and no SAMPLE_LLM_DECISIONS_IMAGE, so the
// pinned released stack never sees them.
func TestDecisionRouteScenariosSkipWithoutDecisionsImage(t *testing.T) {
	scenarios := decisionScenarios(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("REPO_ROOT", "/repo-root-placeholder")
	t.Setenv("NVCF_CLI", "/usr/bin/nvcf-cli")
	t.Setenv(sampleLLMDecisionsImageEnv, "")
	runner := newFakeRunner(map[string]harness.Result{})
	suite := newWiringSuite(t, runner)
	fake := newDecisionRoutesFake("", &llmRoutingHTTPFake{seen: map[string]bool{}})
	suite.HTTP = fake
	sc := steps.NewScenarioContext(suite)
	var out strings.Builder
	status := godog.TestSuite{
		Name:                "decision-routes-opt-in",
		ScenarioInitializer: func(ctx *godog.ScenarioContext) { steps.RegisterAll(ctx, sc) },
		Options: &godog.Options{
			Format: "pretty", Output: &out, Strict: true, Tags: singleClusterHelmfileTags(),
			FeatureContents: []godog.Feature{{Name: "decisions", Contents: []byte("Feature: Decision routes\n" + scenarios)}},
		},
	}.Run()
	if status != 0 {
		t.Fatalf("feature failed without SAMPLE_LLM_DECISIONS_IMAGE, want the scenarios skipped:\n%s", out.String())
	}
	if len(runner.runs) != 0 {
		t.Fatalf("decision scenarios ran commands without SAMPLE_LLM_DECISIONS_IMAGE: %v", runner.runs)
	}

	t.Setenv(sampleLLMDecisionsImageEnv, "nvcr.io/test-org/test-team/openai-compatible-decisions:test")
	if tags := singleClusterHelmfileTags(); tags != "" {
		t.Fatalf("tags with SAMPLE_LLM_DECISIONS_IMAGE set = %q, want every scenario", tags)
	}
}
