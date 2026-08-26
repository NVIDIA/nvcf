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
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

// registerAssertionSteps hooks Then/And forms that read scenario
// state (last command result, file contents) and compare to expected
// values from the feature file.
func registerAssertionSteps(ctx StepRegistrar, sc *ScenarioContext) {
	ctx.Step(`^the command exit code should be (\d+)$`, sc.commandExitCodeShouldBe)
	ctx.Step(`^the command output should contain "([^"]*)"$`, sc.commandOutputShouldContain)
	ctx.Step(`^the command output should not contain "([^"]*)"$`, sc.commandOutputShouldNotContain)
	ctx.Step(`^file "([^"]*)" should exist$`, sc.fileShouldExist)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should equal "([^"]*)"$`, sc.yamlFileKeyShouldEqual)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should not be empty$`, sc.yamlFileKeyShouldNotBeEmpty)
	ctx.Step(`^yaml file "([^"]*)" should have non-empty keys:$`, sc.yamlFileShouldHaveNonEmptyKeys)
	ctx.Step(`^yaml file "([^"]*)" should match:$`, sc.yamlFileShouldMatch)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should match:$`, sc.yamlFileKeyShouldMatch)
	ctx.Step(`^yaml file "([^"]*)" should contain:$`, sc.yamlFileShouldContain)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should contain:$`, sc.yamlFileKeyShouldContain)
	ctx.Step(`^the json output should contain rows:$`, sc.jsonOutputShouldContainRows)
	ctx.Step(`^the JSON command output should contain:$`, sc.jsonCommandOutputShouldContain)
	ctx.Step(`^Helm release "([^"]*)" in namespace "([^"]*)" using context "([^"]*)" should contain values:$`, sc.helmReleaseShouldContainValues)
	ctx.Step(`^the rendered manifests in "([^"]*)" should contain:$`, sc.renderedManifestsShouldContain)
	ctx.Step(`^the rendered manifests in "([^"]*)" under directories matching "([^"]*)" should contain:$`, sc.renderedManifestsUnderMatchingDirectoriesShouldContain)
	ctx.Step(`^the rendered manifests in "([^"]*)" should not contain:$`, sc.renderedManifestsShouldNotContain)
	ctx.Step(`^these Helm releases should be deployed using context "([^"]*)":$`, sc.helmReleasesShouldBeDeployed)
	ctx.Step(`^these Kubernetes resources should exist in namespace "([^"]*)" using context "([^"]*)":$`, sc.kubernetesResourcesShouldExist)
	ctx.Step(`^these Kubernetes resources should not exist in namespace "([^"]*)" using context "([^"]*)":$`, sc.kubernetesResourcesShouldNotExist)
	ctx.Step(`^Kubernetes resource "([^"/]+)/([^"]+)" in namespace "([^"]*)" using context "([^"]*)" should contain:$`, sc.kubernetesResourceShouldContain)
	ctx.Step(`^deployment "([^"]*)" in namespace "([^"]*)" using context "([^"]*)" should complete rollout within "([^"]*)"$`, sc.deploymentShouldCompleteRollout)
	ctx.Step(`^NVCFBackend "([^"]*)" in namespace "([^"]*)" using context "([^"]*)" should report agent status "([^"]*)" within "([^"]*)"$`, sc.nvcfBackendShouldReportAgentStatus)
	ctx.Step(`^these Gateway API routes should be accepted and resolved using context "([^"]*)" within "([^"]*)":$`, sc.gatewayAPIRoutesShouldBeAcceptedAndResolved)
}

func (sc *ScenarioContext) commandExitCodeShouldBe(expected int) error {
	if sc.LastResult.ExitCode != expected {
		// Intentionally do not include stdout/stderr in the error message:
		// rendered manifest output (helmfile template, kubectl get -o yaml)
		// can contain base64-encoded credentials, and the strict-DSL
		// contract keeps captured streams in the per-command log files
		// under Config.CommandLogDir rather than echoing them into test
		// failures. The operator reads <logdir>/<seq>.{stdout,stderr} for
		// the failing command.
		return fmt.Errorf("exit code = %d, want %d (see %s for stdout/stderr)",
			sc.LastResult.ExitCode, expected, sc.Suite.Config.CommandLogDir)
	}
	// On a successful exit-0 assertion, seed the suite-level command
	// cache with the resolved text of the just-run command. This is
	// the When-form counterpart to cachedRun's record-on-success,
	// keyed on the same resolved text. Without it, a later
	// "Given command has succeeded:" for the same command misses the
	// cache and reruns; for install commands that take destructive
	// action on existing releases (helmfile sync that uninstalls then
	// reinstalls nvca-operator, for example), the second run mangles
	// state established by earlier scenarios. Only record on
	// expected==0; negative prechecks ("the command exit code should
	// be 1" against a missing k3d cluster) are not Given-replayable
	// successes.
	if expected == 0 && sc.LastErr == nil && sc.LastCommand != "" {
		sc.Suite.Cache.Record(sc.LastCommand)
	}
	return nil
}

func (sc *ScenarioContext) commandOutputShouldContain(needle string) error {
	combined := combinedOutput(sc.LastResult)
	resolved := dsl.Interpolate(needle)
	if !strings.Contains(combined, resolved) {
		return fmt.Errorf("output does not contain %q", resolved)
	}
	return nil
}

func (sc *ScenarioContext) commandOutputShouldNotContain(needle string) error {
	combined := combinedOutput(sc.LastResult)
	resolved := dsl.Interpolate(needle)
	if strings.Contains(combined, resolved) {
		return fmt.Errorf("output contains %q", resolved)
	}
	return nil
}

func (sc *ScenarioContext) yamlFileKeyShouldEqual(path, key, expected string) error {
	resolvedPath := sc.resolvePath(dsl.Interpolate(path))
	got, found, err := dsl.ReadYAMLKey(resolvedPath, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: key %q not present (%s)", resolvedPath, key, dsl.DescribeMissingKey(resolvedPath, key))
	}
	resolvedExpected := dsl.Interpolate(expected)
	if got != resolvedExpected {
		return fmt.Errorf("%s key %q = %q, want %q", resolvedPath, key, got, resolvedExpected)
	}
	return nil
}

func (sc *ScenarioContext) yamlFileKeyShouldNotBeEmpty(path, key string) error {
	resolvedPath := sc.resolvePath(dsl.Interpolate(path))
	got, found, err := dsl.ReadYAMLKey(resolvedPath, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: key %q not present (%s)", resolvedPath, key, dsl.DescribeMissingKey(resolvedPath, key))
	}
	if got == "" {
		return fmt.Errorf("%s key %q is empty", resolvedPath, key)
	}
	return nil
}

func (sc *ScenarioContext) yamlFileShouldHaveNonEmptyKeys(path string, table *godog.Table) error {
	keys, err := tableToSingleColumn(table, "key")
	if err != nil {
		return err
	}
	return dsl.RequireNonEmptyYAMLKeys(sc.resolvePath(dsl.Interpolate(path)), keys)
}

func (sc *ScenarioContext) yamlFileShouldMatch(path string, doc *godog.DocString) error {
	return dsl.MatchYAMLSubtree(sc.resolvePath(dsl.Interpolate(path)), "", doc.Content, dsl.MatchExact)
}

func (sc *ScenarioContext) yamlFileKeyShouldMatch(path, key string, doc *godog.DocString) error {
	return dsl.MatchYAMLSubtree(sc.resolvePath(dsl.Interpolate(path)), key, doc.Content, dsl.MatchExact)
}

func (sc *ScenarioContext) yamlFileShouldContain(path string, doc *godog.DocString) error {
	return dsl.MatchYAMLSubtree(sc.resolvePath(dsl.Interpolate(path)), "", doc.Content, dsl.MatchSubset)
}

func (sc *ScenarioContext) yamlFileKeyShouldContain(path, key string, doc *godog.DocString) error {
	return dsl.MatchYAMLSubtree(sc.resolvePath(dsl.Interpolate(path)), key, doc.Content, dsl.MatchSubset)
}

func (sc *ScenarioContext) jsonOutputShouldContainRows(table *godog.Table) error {
	rows, err := tableToJSONRows(table)
	if err != nil {
		return err
	}
	return dsl.JSONContainsRows(sc.LastResult.Stdout, rows)
}

func (sc *ScenarioContext) jsonCommandOutputShouldContain(doc *godog.DocString) error {
	return dsl.MatchJSONDocument(sc.LastResult.Stdout, doc.Content, dsl.MatchSubset)
}

func (sc *ScenarioContext) renderedManifestsShouldNotContain(path string, table *godog.Table) error {
	needles, err := tableToSingleColumn(table, "text")
	if err != nil {
		return err
	}
	return dsl.FilesDoNotContain(sc.resolvePath(dsl.Interpolate(path)), needles)
}

func (sc *ScenarioContext) renderedManifestsShouldContain(path string, table *godog.Table) error {
	needles, err := tableToSingleColumn(table, "text")
	if err != nil {
		return err
	}
	return dsl.FilesContain(sc.resolvePath(dsl.Interpolate(path)), "", needles)
}

func (sc *ScenarioContext) renderedManifestsUnderMatchingDirectoriesShouldContain(path, pattern string, table *godog.Table) error {
	needles, err := tableToSingleColumn(table, "text")
	if err != nil {
		return err
	}
	return dsl.FilesContain(sc.resolvePath(dsl.Interpolate(path)), pattern, needles)
}

func tableToSingleColumn(table *godog.Table, header string) ([]string, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have a %q header and at least one data row", header)
	}
	if len(table.Rows[0].Cells) != 1 || strings.TrimSpace(table.Rows[0].Cells[0].Value) != header {
		return nil, fmt.Errorf("table header must be %q", header)
	}
	values := make([]string, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != 1 {
			return nil, fmt.Errorf("row %d has %d cells, expected exactly 1", index+1, len(row.Cells))
		}
		value := row.Cells[0].Value
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("row %d has an empty %s value", index+1, header)
		}
		values = append(values, value)
	}
	return values, nil
}

func (sc *ScenarioContext) helmReleasesShouldBeDeployed(ctx context.Context, kubeContext string, table *godog.Table) error {
	expected, err := tableToHelmReleaseExpectations(table)
	if err != nil {
		return err
	}
	command, err := dsl.HelmListCommand(kubeContext)
	if err != nil {
		return err
	}
	if err := sc.runAndRecord(ctx, command); err != nil {
		return err
	}
	if err := sc.commandExitCodeShouldBe(0); err != nil {
		return err
	}
	return dsl.HelmReleasesDeployed(sc.LastResult.Stdout, expected)
}

func (sc *ScenarioContext) helmReleaseShouldContainValues(ctx context.Context, release, namespace, kubeContext string, doc *godog.DocString) error {
	command, err := dsl.HelmReleaseValuesCommand(release, namespace, kubeContext)
	if err != nil {
		return err
	}
	if err := sc.runAndRecord(ctx, command); err != nil {
		return err
	}
	resolvedRelease := dsl.Interpolate(release)
	resolvedNamespace := dsl.Interpolate(namespace)
	if err := sc.commandExitCodeShouldBe(0); err != nil {
		return fmt.Errorf("helm release %q in namespace %q values could not be read: %w", resolvedRelease, resolvedNamespace, err)
	}
	if err := dsl.MatchYAMLDocument(sc.LastResult.Stdout, doc.Content, dsl.MatchSubset); err != nil {
		return fmt.Errorf("helm release %q in namespace %q values do not contain the expected YAML subset: %w", resolvedRelease, resolvedNamespace, err)
	}
	return nil
}

func tableToHelmReleaseExpectations(table *godog.Table) ([]dsl.HelmReleaseExpectation, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have name and namespace headers and at least one data row")
	}
	headers := table.Rows[0].Cells
	withRevision := len(headers) == 3
	if len(headers) != 2 && !withRevision {
		return nil, fmt.Errorf("table headers must be name, namespace, and optional revision")
	}
	if strings.TrimSpace(headers[0].Value) != "name" || strings.TrimSpace(headers[1].Value) != "namespace" || withRevision && strings.TrimSpace(headers[2].Value) != "revision" {
		return nil, fmt.Errorf("table headers must be name, namespace, and optional revision")
	}

	expected := make([]dsl.HelmReleaseExpectation, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, fmt.Errorf("row %d has %d cells, expected %d", index+1, len(row.Cells), len(headers))
		}
		release := dsl.HelmReleaseExpectation{
			Name:      row.Cells[0].Value,
			Namespace: row.Cells[1].Value,
		}
		if withRevision {
			release.Revision = row.Cells[2].Value
		}
		expected = append(expected, release)
	}
	return expected, nil
}

func (sc *ScenarioContext) kubernetesResourcesShouldExist(ctx context.Context, namespace, kubeContext string, table *godog.Table) error {
	resources, err := tableToKubernetesResources(table)
	if err != nil {
		return err
	}
	for index, resource := range resources {
		command, err := dsl.KubernetesResourceGetCommand(namespace, kubeContext, resource, false)
		if err != nil {
			return fmt.Errorf("row %d (%s/%s): %w", index+1, resource.Kind, resource.Name, err)
		}
		if err := sc.runAndRecord(ctx, command); err != nil {
			return fmt.Errorf("row %d (%s/%s): %w", index+1, resource.Kind, resource.Name, err)
		}
		if err := sc.commandExitCodeShouldBe(0); err != nil {
			return fmt.Errorf("row %d: Kubernetes resource %s/%s should exist: %w", index+1, resource.Kind, resource.Name, err)
		}
	}
	return nil
}

func (sc *ScenarioContext) kubernetesResourcesShouldNotExist(ctx context.Context, namespace, kubeContext string, table *godog.Table) error {
	resources, err := tableToKubernetesResources(table)
	if err != nil {
		return err
	}
	for index, resource := range resources {
		command, err := dsl.KubernetesResourceGetCommand(namespace, kubeContext, resource, true)
		if err != nil {
			return fmt.Errorf("row %d (%s/%s): %w", index+1, resource.Kind, resource.Name, err)
		}
		if err := sc.runAndRecord(ctx, command); err != nil {
			return fmt.Errorf("row %d (%s/%s): %w", index+1, resource.Kind, resource.Name, err)
		}
		if err := sc.commandExitCodeShouldBe(0); err != nil {
			return fmt.Errorf("row %d: Kubernetes resource %s/%s absence check failed: %w", index+1, resource.Kind, resource.Name, err)
		}
		if err := dsl.KubernetesResourceAbsent(sc.LastResult.Stdout, resource); err != nil {
			return fmt.Errorf("row %d: %w", index+1, err)
		}
	}
	return nil
}

func (sc *ScenarioContext) kubernetesResourceShouldContain(ctx context.Context, kind, name, namespace, kubeContext string, doc *godog.DocString) error {
	resource := dsl.KubernetesResource{Kind: kind, Name: name}
	command, err := dsl.KubernetesResourceYAMLGetCommand(namespace, kubeContext, resource)
	if err != nil {
		return err
	}
	if err := sc.runAndRecord(ctx, command); err != nil {
		return err
	}
	if err := sc.commandExitCodeShouldBe(0); err != nil {
		return fmt.Errorf("kubernetes resource %s/%s could not be read: %w", kind, name, err)
	}
	if err := dsl.MatchYAMLDocument(sc.LastResult.Stdout, doc.Content, dsl.MatchSubset); err != nil {
		return fmt.Errorf("kubernetes resource %s/%s does not contain the expected YAML subset: %w", kind, name, err)
	}
	return nil
}

func (sc *ScenarioContext) deploymentShouldCompleteRollout(ctx context.Context, name, namespace, kubeContext, timeout string) error {
	command, err := dsl.KubernetesDeploymentRolloutCommand(name, namespace, kubeContext, timeout)
	if err != nil {
		return err
	}
	if err := sc.runSuccessfully(ctx, command); err != nil {
		return fmt.Errorf("deployment %q did not complete rollout: %w", dsl.Interpolate(name), err)
	}
	return nil
}

func (sc *ScenarioContext) nvcfBackendShouldReportAgentStatus(ctx context.Context, name, namespace, kubeContext, agentStatus, timeout string) error {
	command, err := dsl.NVCFBackendAgentStatusCommand(name, namespace, kubeContext, agentStatus, timeout)
	if err != nil {
		return err
	}
	if err := sc.runSuccessfully(ctx, command); err != nil {
		return fmt.Errorf("NVCFBackend %q did not report agent status %q: %w", dsl.Interpolate(name), dsl.Interpolate(agentStatus), err)
	}
	return nil
}

func (sc *ScenarioContext) gatewayAPIRoutesShouldBeAcceptedAndResolved(ctx context.Context, kubeContext, timeout string, table *godog.Table) error {
	routes, err := tableToGatewayAPIRoutes(table)
	if err != nil {
		return err
	}
	waits, err := dsl.GatewayAPIRouteReadinessWaits(routes, kubeContext, timeout)
	if err != nil {
		return err
	}

	for _, wait := range waits {
		if err := sc.runResolvedSuccessfully(ctx, wait.Command); err != nil {
			return fmt.Errorf(
				"row %d: Gateway API route %s/%s in namespace %q for parent %q did not report condition %q: %w",
				wait.Row,
				wait.Route.Kind,
				wait.Route.Name,
				wait.Route.Namespace,
				wait.Route.Parent,
				wait.Condition,
				err,
			)
		}
	}
	return nil
}

func tableToKubernetesResources(table *godog.Table) ([]dsl.KubernetesResource, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have kind and name headers and at least one data row")
	}
	headers := table.Rows[0].Cells
	if len(headers) != 2 || strings.TrimSpace(headers[0].Value) != "kind" || strings.TrimSpace(headers[1].Value) != "name" {
		return nil, fmt.Errorf("table headers must be kind and name")
	}

	resources := make([]dsl.KubernetesResource, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, fmt.Errorf("row %d has %d cells, expected %d", index+1, len(row.Cells), len(headers))
		}
		resource := dsl.KubernetesResource{
			Kind: strings.TrimSpace(dsl.Interpolate(row.Cells[0].Value)),
			Name: strings.TrimSpace(dsl.Interpolate(row.Cells[1].Value)),
		}
		if resource.Kind == "" {
			return nil, fmt.Errorf("row %d has an empty kind", index+1)
		}
		if resource.Name == "" {
			return nil, fmt.Errorf("row %d has an empty name", index+1)
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func tableToGatewayAPIRoutes(table *godog.Table) ([]dsl.GatewayAPIRoute, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have kind, name, namespace, and parent headers and at least one data row")
	}
	headers := table.Rows[0].Cells
	if len(headers) != 4 ||
		strings.TrimSpace(headers[0].Value) != "kind" ||
		strings.TrimSpace(headers[1].Value) != "name" ||
		strings.TrimSpace(headers[2].Value) != "namespace" ||
		strings.TrimSpace(headers[3].Value) != "parent" {
		return nil, fmt.Errorf("table headers must be kind, name, namespace, and parent")
	}

	routes := make([]dsl.GatewayAPIRoute, 0, len(table.Rows)-1)
	for index, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers) {
			return nil, fmt.Errorf("row %d has %d cells, expected %d", index+1, len(row.Cells), len(headers))
		}
		route := dsl.GatewayAPIRoute{
			Kind:      strings.TrimSpace(dsl.Interpolate(row.Cells[0].Value)),
			Name:      strings.TrimSpace(dsl.Interpolate(row.Cells[1].Value)),
			Namespace: strings.TrimSpace(dsl.Interpolate(row.Cells[2].Value)),
			Parent:    strings.TrimSpace(dsl.Interpolate(row.Cells[3].Value)),
		}
		if route.Kind == "" {
			return nil, fmt.Errorf("row %d has an empty kind", index+1)
		}
		if route.Name == "" {
			return nil, fmt.Errorf("row %d has an empty name", index+1)
		}
		if route.Namespace == "" {
			return nil, fmt.Errorf("row %d has an empty namespace", index+1)
		}
		if route.Parent == "" {
			return nil, fmt.Errorf("row %d has an empty parent", index+1)
		}
		routes = append(routes, route)
	}
	return routes, nil
}

// tableToJSONRows converts a header-first Godog table into a slice of
// row maps keyed by column name.
func tableToJSONRows(table *godog.Table) ([]map[string]string, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, fmt.Errorf("table must have a header row and at least one data row")
	}
	headers := table.Rows[0]
	out := make([]map[string]string, 0, len(table.Rows)-1)
	for _, row := range table.Rows[1:] {
		if len(row.Cells) != len(headers.Cells) {
			return nil, fmt.Errorf("row has %d cells, header has %d", len(row.Cells), len(headers.Cells))
		}
		entry := make(map[string]string, len(headers.Cells))
		for i, cell := range row.Cells {
			entry[headers.Cells[i].Value] = cell.Value
		}
		out = append(out, entry)
	}
	return out, nil
}
