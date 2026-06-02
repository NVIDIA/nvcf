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
	"regexp"
	"strings"

	"github.com/cucumber/godog"

	"nvcf-bdd/dsl"
)

const (
	singleClusterMakeCommand    = "make -C tools/ncp-local-cluster build-and-deploy-cluster"
	multiClusterMakeCommandFmt  = "make -C tools/ncp-local-cluster build-and-deploy-multicluster COMPUTE_CLUSTER_COUNT=%d"
	computeClusterNamePattern   = `^ncp-local-compute-\d+$`
	controlPlaneClusterReserved = "ncp-local-cp"
)

var computeClusterRE = regexp.MustCompile(computeClusterNamePattern)

// registerInfraSteps hooks the three infrastructure-bootstrap Givens
// spelled out in PLAN.md. Each wraps exactly one Make target or one
// composite of kubectl calls; the bootstrap make commands are cached
// per suite so a feature with N scenarios re-runs the cluster build
// at most once.
func registerInfraSteps(ctx *godog.ScenarioContext, sc *ScenarioContext) {
	ctx.Step(`^a single-cluster ncp-local cluster is running$`, sc.singleClusterIsRunning)
	ctx.Step(`^multi-cluster ncp-local compute clusters are running:$`, sc.multiClusterComputeRunning)
	ctx.Step(`^the "([^"]*)" image pull secret exists in namespaces:$`, sc.pullSecretInNamespaces)
}

func (sc *ScenarioContext) singleClusterIsRunning(ctx context.Context) error {
	return sc.cachedRun(ctx, singleClusterMakeCommand)
}

// multiClusterComputeRunning rejects rows that name the implicit
// control plane (ncp-local-cp) and rows that do not match the
// ncp-local-compute-<N> shape so a Gherkin typo cannot quietly bring
// up a differently named cluster.
func (sc *ScenarioContext) multiClusterComputeRunning(ctx context.Context, table *godog.Table) error {
	if table == nil || len(table.Rows) == 0 {
		return fmt.Errorf("compute cluster table is empty")
	}
	for i, row := range table.Rows {
		if len(row.Cells) != 1 {
			return fmt.Errorf("row %d has %d cells, expected exactly 1", i, len(row.Cells))
		}
		name := strings.TrimSpace(row.Cells[0].Value)
		if name == controlPlaneClusterReserved {
			return fmt.Errorf("table must not list the implicit control plane %q", name)
		}
		if !computeClusterRE.MatchString(name) {
			return fmt.Errorf("row %q does not match %s", name, computeClusterNamePattern)
		}
	}
	command := fmt.Sprintf(multiClusterMakeCommandFmt, len(table.Rows))
	return sc.cachedRun(ctx, command)
}

// pullSecretInNamespaces applies a namespace manifest and a
// docker-registry secret manifest for every row. The API key never
// appears in argv; the manifests are built by dsl helpers and written
// to temp files in the suite's output dir so failed runs leave the
// artifacts under tests/bdd/out/<run-id>/ for inspection.
//
// The two manifests are applied per-namespace in order (namespace then
// secret) because the secret targets a specific namespace. Do not
// collapse into a single multi-document manifest: some kubectl
// versions decompose the resources into argv during processing, which
// can re-expose the API key.
func (sc *ScenarioContext) pullSecretInNamespaces(ctx context.Context, secretName string, table *godog.Table) error {
	if table == nil || len(table.Rows) == 0 {
		return fmt.Errorf("namespaces table is empty")
	}
	apiKey := os.Getenv("NGC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("NGC_API_KEY is not set")
	}
	for _, row := range table.Rows {
		if len(row.Cells) != 1 {
			return fmt.Errorf("namespace row must have one cell")
		}
		ns := strings.TrimSpace(row.Cells[0].Value)
		nsBody, err := dsl.NamespaceManifest(ns)
		if err != nil {
			return fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		if err := sc.applyManifest(ctx, nsBody); err != nil {
			return fmt.Errorf("ensure namespace %s: %w", ns, err)
		}
		secretBody, err := dsl.DockerConfigJSONSecretManifest(secretName, ns, apiKey)
		if err != nil {
			return fmt.Errorf("build pull secret manifest in %s: %w", ns, err)
		}
		if err := sc.applyManifest(ctx, secretBody); err != nil {
			return fmt.Errorf("apply pull secret in %s: %w", ns, err)
		}
	}
	return nil
}

// applyManifest writes body to a temp file inside the run's OutDir and
// runs kubectl apply against it. Routing through OutDir (rather than
// /tmp) means failed runs leave the artifacts in the run directory
// alongside command logs for post-mortem inspection.
func (sc *ScenarioContext) applyManifest(ctx context.Context, body []byte) error {
	dir := sc.Suite.Config.OutDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure manifest dir: %w", err)
	}
	file, err := os.CreateTemp(dir, "manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("create manifest file: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(body); err != nil {
		_ = file.Close()
		return fmt.Errorf("write manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	_, err = sc.Suite.Runner.Run(ctx, fmt.Sprintf("kubectl apply -f %s", path))
	return err
}
