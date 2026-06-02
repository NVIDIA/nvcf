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
	"fmt"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

// registerAssertionSteps hooks Then/And forms that read scenario
// state (last command result, file contents) and compare to expected
// values from the feature file.
func registerAssertionSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^the command exit code should be (\d+)$`, sc.commandExitCodeShouldBe)
	ctx.Step(`^the command output should contain "([^"]*)"$`, sc.commandOutputShouldContain)
	ctx.Step(`^the command output should not contain "([^"]*)"$`, sc.commandOutputShouldNotContain)
	ctx.Step(`^file "([^"]*)" should exist$`, sc.fileShouldExist)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should equal "([^"]*)"$`, sc.yamlFileKeyShouldEqual)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should not be empty$`, sc.yamlFileKeyShouldNotBeEmpty)
	ctx.Step(`^yaml file "([^"]*)" should match:$`, sc.yamlFileShouldMatch)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should match:$`, sc.yamlFileKeyShouldMatch)
	ctx.Step(`^yaml file "([^"]*)" should contain:$`, sc.yamlFileShouldContain)
	ctx.Step(`^yaml file "([^"]*)" key "([^"]*)" should contain:$`, sc.yamlFileKeyShouldContain)
	ctx.Step(`^the json output should contain rows:$`, sc.jsonOutputShouldContainRows)
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
