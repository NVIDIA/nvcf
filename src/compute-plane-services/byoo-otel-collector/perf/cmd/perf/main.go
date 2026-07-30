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

// Command perf is the entrypoint for the BYOO collector performance test
// suite. In this first milestone only "render" is fully wired; "run" and
// "cleanup" are scaffolding for the deployment/load milestones (S4+).
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/profile"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/spec"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/validate"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "perf",
		Short: "BYOO OpenTelemetry collector performance test suite",
		Long: `perf renders, validates, and (in later milestones) runs performance tests
for the BYOO OpenTelemetry collector using the same workload shape produced in
production by the shared icms-translate library.`,
		SilenceUsage: true,
	}
	root.AddCommand(newRenderCmd(), newRunCmd(), newCleanupCmd())
	return root
}

// renderConfig holds the resolved flags for the render command.
type renderConfig struct {
	shape          string
	profile        string
	collectorImage string
	namespace      string
	output         string
}

func newRenderCmd() *cobra.Command {
	var cfg renderConfig
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render and validate the production workload shape (no cluster required)",
		Long: `render translates a synthetic NVCF function launch spec through
icms-translate, extracts the authentic BYOO collector, and validates its shape.
It runs entirely locally: it does not connect to a cluster or use kubectl.

In "yaml" and "json" output modes, only the rendered manifest is written to
stdout (diagnostics go to stderr) so the output can be piped to kubectl or a
parser. "yaml" emits a multi-document stream and "json" emits an array, so
--shape both stays valid.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRender(cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.shape, "shape", "both", `deployment shape: "container", "helm", or "both"`)
	cmd.Flags().StringVar(&cfg.profile, "profile", "dev", `execution profile: "dev" or "baseline"`)
	cmd.Flags().StringVar(&cfg.collectorImage, "collector-image", spec.DefaultCollectorImage, "BYOO collector image reference")
	cmd.Flags().StringVar(&cfg.namespace, "namespace", "byoo-perf", "namespace for rendered objects")
	cmd.Flags().StringVar(&cfg.output, "output", "summary", `output format: "summary", "yaml", or "json"`)
	return cmd
}

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Deploy, drive load, and measure (not yet implemented; see S4+)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("`run` is not implemented yet; it lands with the deployment/load milestones (S4-S9)")
		},
	}
	cmd.Flags().String("shape", "both", `deployment shape: "container", "helm", or "both"`)
	cmd.Flags().String("profile", "dev", `execution profile: "dev" or "baseline"`)
	cmd.Flags().String("mode", "k3d", `deployment mode: "k3d" or "remote"`)
	cmd.Flags().Bool("retain", false, "retain test resources for debugging instead of cleaning up")
	return cmd
}

func newCleanupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove test resources (not yet implemented; see S5+)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("`cleanup` is not implemented yet; it lands with the deployment milestone (S5)")
		},
	}
	cmd.Flags().String("mode", "k3d", `deployment mode: "k3d" or "remote"`)
	cmd.Flags().String("namespace", "byoo-perf", "namespace to clean up")
	return cmd
}

func runRender(stdout, stderr io.Writer, cfg renderConfig) error {
	switch cfg.output {
	case "summary", "yaml", "json":
	default:
		return fmt.Errorf("unknown output %q (want \"summary\", \"yaml\", or \"json\")", cfg.output)
	}

	prof, err := profile.Lookup(cfg.profile)
	if err != nil {
		return err
	}
	shapes, err := shapesFromFlag(cfg.shape)
	if err != nil {
		return err
	}

	opts := spec.DefaultOptions()
	opts.Namespace = cfg.namespace
	opts.CollectorImage = cfg.collectorImage

	exp := validate.Expectations{
		Image:     opts.CollectorImage,
		Resources: common.GetDefaultContainerResourcesBYOO(),
	}

	// Diagnostics go to stderr so stdout stays a clean machine-readable
	// document in yaml/json modes.
	fmt.Fprintf(stderr, "profile=%s warmup=%s window=%s reps=%d\n\n", prof.Name, prof.Warmup, prof.MeasurementWindow, prof.Repetitions)

	results := make([]*render.Result, 0, len(shapes))
	for _, shape := range shapes {
		res, err := render.Render(shape, opts)
		if err != nil {
			return fmt.Errorf("render %s: %w", shape, err)
		}
		if err := validate.Render(res, exp); err != nil {
			return err
		}
		results = append(results, res)
	}

	switch cfg.output {
	case "summary":
		for _, res := range results {
			printSummary(stdout, res)
		}
	case "yaml":
		return printYAML(stdout, stderr, results, cfg.namespace)
	case "json":
		return printJSON(stdout, results, cfg.namespace)
	}
	return nil
}

func printSummary(w io.Writer, res *render.Result) {
	fmt.Fprintf(w, "[%s] VALID\n", res.Shape)
	fmt.Fprintf(w, "  collector image : %s\n", res.Collector.Image)
	fmt.Fprintf(w, "  config version  : %s\n", res.OTelVersion)
	fmt.Fprintf(w, "  owner pod       : %s\n", res.OwnerPod)
	if res.Service != nil {
		fmt.Fprintf(w, "  otlp service    : %s\n", res.Service.Name)
	}
	fmt.Fprintf(w, "  ports           : %s\n", portSummary(res))
	fmt.Fprintf(w, "  objects         : %d translated\n\n", len(res.Objects))
}

func portSummary(res *render.Result) string {
	parts := make([]string, 0, len(res.Collector.Ports))
	for _, p := range res.Collector.Ports {
		parts = append(parts, fmt.Sprintf("%s:%d", p.Name, p.ContainerPort))
	}
	return strings.Join(parts, " ")
}

// printYAML writes the bench pods as a multi-document YAML stream so that
// --shape both remains a valid manifest kubectl can apply. The per-shape
// annotation is written to stderr as a comment, keeping stdout parseable.
func printYAML(stdout, stderr io.Writer, results []*render.Result, namespace string) error {
	for i, res := range results {
		out, err := yaml.Marshal(res.BenchPod(namespace))
		if err != nil {
			return fmt.Errorf("marshal bench pod: %w", err)
		}
		fmt.Fprintf(stderr, "# shape=%s benchmark workload (authentic collector + emptyDir stand-ins)\n", res.Shape)
		if i > 0 {
			fmt.Fprintln(stdout, "---")
		}
		fmt.Fprintf(stdout, "%s", out)
	}
	return nil
}

// printJSON writes the bench pods as a JSON array so that multiple shapes emit
// a single valid JSON document.
func printJSON(stdout io.Writer, results []*render.Result, namespace string) error {
	pods := make([]*corev1.Pod, 0, len(results))
	for _, res := range results {
		pods = append(pods, res.BenchPod(namespace))
	}
	y, err := yaml.Marshal(pods)
	if err != nil {
		return fmt.Errorf("marshal bench pods: %w", err)
	}
	j, err := yaml.YAMLToJSON(y)
	if err != nil {
		return fmt.Errorf("convert to json: %w", err)
	}
	fmt.Fprintf(stdout, "%s\n", j)
	return nil
}

func shapesFromFlag(s string) ([]spec.Shape, error) {
	switch s {
	case "container":
		return []spec.Shape{spec.ShapeContainer}, nil
	case "helm":
		return []spec.Shape{spec.ShapeHelm}, nil
	case "both":
		return []spec.Shape{spec.ShapeContainer, spec.ShapeHelm}, nil
	default:
		return nil, fmt.Errorf("unknown shape %q (want \"container\", \"helm\", or \"both\")", s)
	}
}
