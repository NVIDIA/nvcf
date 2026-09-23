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
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

// registerManifestSteps hooks the named Kubernetes manifest steps. The
// Given stores the visible docstring under a scenario-scoped name; the When
// interpolates it, writes it under the run's OutDir, and applies it once per
// listed context. The pair hides only the repeated kubectl apply mechanics.
func registerManifestSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^Kubernetes manifest "([^"]*)" is:$`, sc.kubernetesManifestIs)
	ctx.Step(`^I successfully apply Kubernetes manifest "([^"]*)" using contexts:$`, sc.iSuccessfullyApplyKubernetesManifest)
}

// kubernetesManifestIs records the raw docstring. Interpolation is deferred
// to apply time so the manifest may reference an env var exported by a
// later step. Redeclaring a name in the same scenario is an error so two
// docstrings cannot silently compete for one apply.
func (sc *ScenarioContext) kubernetesManifestIs(name string, doc *godog.DocString) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("kubernetes manifest name is empty")
	}
	if doc == nil || strings.TrimSpace(doc.Content) == "" {
		return fmt.Errorf("kubernetes manifest %q has an empty body", name)
	}
	if _, exists := sc.Manifests[name]; exists {
		return fmt.Errorf("kubernetes manifest %q is already declared in this scenario", name)
	}
	if sc.Manifests == nil {
		sc.Manifests = map[string]string{}
	}
	sc.Manifests[name] = doc.Content
	return nil
}

// iSuccessfullyApplyKubernetesManifest writes the interpolated manifest once
// and runs one explicit-context kubectl apply per context row. Each apply
// must exit 0; the failure names the row, manifest, and context.
func (sc *ScenarioContext) iSuccessfullyApplyKubernetesManifest(ctx context.Context, name string, table *godog.Table) error {
	name = strings.TrimSpace(name)
	body, declared := sc.Manifests[name]
	if !declared {
		return fmt.Errorf("kubernetes manifest %q is not declared in this scenario", name)
	}
	contexts, err := tableToSingleColumn(table, "context")
	if err != nil {
		return err
	}
	path, err := sc.writeManifestFile(name, []byte(dsl.Interpolate(body)))
	if err != nil {
		return fmt.Errorf("kubernetes manifest %q: %w", name, err)
	}
	for index, kubeContext := range contexts {
		command, err := dsl.KubectlApplyCommand(path, kubeContext)
		if err != nil {
			return fmt.Errorf("row %d: kubernetes manifest %q: %w", index+1, name, err)
		}
		if err := sc.runResolvedSuccessfully(ctx, command); err != nil {
			return fmt.Errorf(
				"row %d: kubernetes manifest %q was not applied using context %q: %w",
				index+1, name, strings.TrimSpace(dsl.Interpolate(kubeContext)), err,
			)
		}
	}
	return nil
}

// writeManifestFile writes body to a new file inside the run's OutDir and
// returns its path. Routing through OutDir (rather than /tmp) means failed
// runs leave the artifacts in the run directory alongside command logs for
// post-mortem inspection. The label becomes part of the file name so an
// operator can match a manifest file to the step that produced it.
func (sc *ScenarioContext) writeManifestFile(label string, body []byte) (string, error) {
	dir := sc.Suite.Config.OutDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("ensure manifest dir: %w", err)
	}
	file, err := os.CreateTemp(dir, "manifest-"+manifestFileLabel(label)+"-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create manifest file: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close manifest: %w", err)
	}
	return path, nil
}

// manifestFileLabel keeps only characters that are safe in a file name.
func manifestFileLabel(label string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return '-'
		}
	}, label)
}
