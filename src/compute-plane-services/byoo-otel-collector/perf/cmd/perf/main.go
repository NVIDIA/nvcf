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
// suite. "render" validates the production workload shape with no cluster,
// "run" deploys the authentic collector and waits for it to become ready, and
// "cleanup" removes the resources the suite created. Load generation and
// measurement land in a later milestone.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/deploy"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/loadgen"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/profile"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/render"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/perf/pkg/sink"
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

// runConfig holds the resolved flags for the run command.
type runConfig struct {
	shape          string
	profile        string
	mode           string
	collectorImage string
	sinkImage      string
	loadgenImage   string
	namespace      string
	kubeconfig     string
	kubeContext    string
	readyTimeout   time.Duration
	retain         bool
	skipLoad       bool
}

func newRunCmd() *cobra.Command {
	var cfg runConfig
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Deploy the collector + OTLP sink, drive load, and wait until it is ready",
		Long: `run renders the production workload shape via icms-translate, validates it,
deploys an in-cluster OTLP sink, deploys the authentic BYOO collector pointed at
that sink, waits for both to become ready, and drives telemetrygen load at the
selected profile's rates. It cleans up afterwards unless --retain is set.
Measurement and reporting land in a later milestone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.shape, "shape", "both", `deployment shape: "container", "helm", or "both"`)
	cmd.Flags().StringVar(&cfg.profile, "profile", "dev", `execution profile: "dev" or "baseline"`)
	cmd.Flags().StringVar(&cfg.mode, "mode", "k3d", `deployment mode: "k3d" or "remote"`)
	cmd.Flags().StringVar(&cfg.collectorImage, "collector-image", spec.DefaultCollectorImage, "BYOO collector image reference")
	cmd.Flags().StringVar(&cfg.sinkImage, "sink-image", sink.DefaultImage, "OTLP sink (collector-contrib) image reference")
	cmd.Flags().StringVar(&cfg.loadgenImage, "loadgen-image", loadgen.DefaultImage, "telemetrygen load generator image reference")
	cmd.Flags().StringVar(&cfg.namespace, "namespace", "byoo-perf", "base namespace for deployed resources")
	cmd.Flags().StringVar(&cfg.kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to in-cluster or $KUBECONFIG)")
	cmd.Flags().StringVar(&cfg.kubeContext, "context", "", "kubeconfig context to use")
	cmd.Flags().DurationVar(&cfg.readyTimeout, "ready-timeout", 3*time.Minute, "how long to wait for the collector and sink to become ready")
	cmd.Flags().BoolVar(&cfg.retain, "retain", false, "retain deployed resources instead of cleaning up after the run")
	cmd.Flags().BoolVar(&cfg.skipLoad, "skip-load", false, "deploy the collector and sink but do not drive load")
	return cmd
}

// cleanupConfig holds the resolved flags for the cleanup command.
type cleanupConfig struct {
	shape       string
	namespace   string
	kubeconfig  string
	kubeContext string
}

func newCleanupCmd() *cobra.Command {
	var cfg cleanupConfig
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove the resources the suite created in a namespace",
		Long: `cleanup deletes every pod and service the suite created, scoped by the
suite's part-of label so it never removes unrelated resources.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCleanup(cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.shape, "shape", "both", `shape namespaces to clean: "container", "helm", or "both"`)
	cmd.Flags().StringVar(&cfg.namespace, "namespace", "byoo-perf", "base namespace to clean up")
	cmd.Flags().StringVar(&cfg.kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to in-cluster or $KUBECONFIG)")
	cmd.Flags().StringVar(&cfg.kubeContext, "context", "", "kubeconfig context to use")
	return cmd
}

func runRun(stdout io.Writer, cfg runConfig) error {
	if cfg.mode != "k3d" && cfg.mode != "remote" {
		return fmt.Errorf("unknown mode %q (want \"k3d\" or \"remote\")", cfg.mode)
	}
	prof, err := profile.Lookup(cfg.profile)
	if err != nil {
		return err
	}
	shapes, err := shapesFromFlag(cfg.shape)
	if err != nil {
		return err
	}

	client, err := deploy.NewClient(cfg.kubeconfig, cfg.kubeContext)
	if err != nil {
		return err
	}

	ctx := context.Background()
	loadDuration := prof.Warmup + prof.MeasurementWindow
	fmt.Fprintf(stdout, "mode=%s profile=%s warmup=%s window=%s reps=%d\n\n", cfg.mode, prof.Name, prof.Warmup, prof.MeasurementWindow, prof.Repetitions)

	multi := len(shapes) > 1
	for _, shape := range shapes {
		if err := runShape(ctx, stdout, client, cfg, prof, shape, multi, loadDuration); err != nil {
			return err
		}
	}

	if cfg.skipLoad {
		fmt.Fprintln(stdout, "note: --skip-load set; the collector and sink were deployed but no load was driven. Measurement and reporting land in a later milestone.")
	} else {
		fmt.Fprintln(stdout, "note: load was driven end-to-end through the collector to the in-cluster sink. Measurement and reporting land in a later milestone.")
	}
	return nil
}

// runShape deploys the sink and collector for one shape, drives load, and cleans
// up (unless --retain). The collector's export is redirected at the in-cluster
// sink so telemetry drains during the run instead of backing up against the
// unreachable placeholder endpoints used purely for rendering.
func runShape(ctx context.Context, stdout io.Writer, client *deploy.Client, cfg runConfig, prof profile.Profile, shape spec.Shape, multi bool, loadDuration time.Duration) error {
	ns := namespaceForShape(cfg.namespace, shape, multi)

	// 1. In-cluster OTLP sink the collector exports to.
	fmt.Fprintf(stdout, "[%s] deploying OTLP sink to namespace %q ...\n", shape, ns)
	sinkDep, err := client.DeploySink(ctx, ns, sink.Options{Image: cfg.sinkImage})
	if err != nil {
		return fmt.Errorf("deploy sink for %s: %w", shape, err)
	}
	if err := client.WaitPodReady(ctx, ns, sinkDep.PodName, cfg.readyTimeout); err != nil {
		return cleanupAfterErr(ctx, stdout, client, cfg, ns, shape, fmt.Errorf("sink did not become ready for %s shape: %w", shape, err))
	}

	// 2. Authentic collector, rendered with its export pointed at the sink.
	opts := spec.DefaultOptions()
	opts.Namespace = ns
	opts.CollectorImage = cfg.collectorImage
	// OTEL_COLLECTOR uses a plain otlp_http exporter with a single bearer-token
	// file per signal, which the sink accepts and ignores; this is the
	// lowest-friction way to make the collector export succeed in-cluster.
	opts.Provider = "OTEL_COLLECTOR"
	opts.Protocol = "http"
	opts.LogsEndpoint = sinkDep.HTTPEndpoint
	opts.MetricsEndpoint = sinkDep.HTTPEndpoint

	res, err := render.Render(shape, opts)
	if err != nil {
		return fmt.Errorf("render %s: %w", shape, err)
	}
	exp := validate.Expectations{
		Image:     opts.CollectorImage,
		Resources: common.GetDefaultContainerResourcesBYOO(),
	}
	if err := validate.Render(res, exp); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "[%s] deploying collector to namespace %q ...\n", shape, ns)
	dep, err := client.Deploy(ctx, ns, res, deploy.WithExportCredentials(exportCredentials()))
	if err != nil {
		return cleanupAfterErr(ctx, stdout, client, cfg, ns, shape, fmt.Errorf("deploy %s: %w", shape, err))
	}

	fmt.Fprintf(stdout, "[%s] waiting up to %s for collector pod %q to become ready ...\n", shape, cfg.readyTimeout, dep.PodName)
	if err := client.WaitPodReady(ctx, ns, dep.PodName, cfg.readyTimeout); err != nil {
		return cleanupAfterErr(ctx, stdout, client, cfg, ns, shape, fmt.Errorf("collector did not become ready for %s shape: %w", shape, err))
	}

	fmt.Fprintf(stdout, "[%s] READY\n", shape)
	fmt.Fprintf(stdout, "  collector pod   : %s\n", dep.PodName)
	fmt.Fprintf(stdout, "  otlp service    : %s\n", dep.ServiceName)
	for _, name := range []string{"otlp-grpc", "otlp-http"} {
		if ep, ok := dep.Endpoints[name]; ok {
			fmt.Fprintf(stdout, "  %-15s : %s\n", name, ep)
		}
	}
	fmt.Fprintf(stdout, "  sink metrics    : %s\n", sinkDep.MetricsEndpoint)

	// 3. Drive load through the collector.
	if !cfg.skipLoad {
		grpcEndpoint := dep.Endpoints["otlp-grpc"]
		if grpcEndpoint == "" {
			return cleanupAfterErr(ctx, stdout, client, cfg, ns, shape, fmt.Errorf("collector has no otlp-grpc endpoint for %s shape", shape))
		}
		lgOpts := loadgen.Options{
			Image:         cfg.loadgenImage,
			Endpoint:      grpcEndpoint,
			Insecure:      true,
			Duration:      loadDuration,
			LogsPerSec:    prof.LogRecordsPerSec,
			MetricsPerSec: prof.MetricDataPointsPerSec,
		}
		jobs := loadgen.Jobs(ns, dep.PodName, lgOpts)
		fmt.Fprintf(stdout, "[%s] driving load for %s (logs=%d/s metrics=%d/s) ...\n", shape, loadDuration, lgOpts.LogsPerSec, lgOpts.MetricsPerSec)
		if err := client.RunLoad(ctx, ns, jobs, loadDuration+cfg.readyTimeout); err != nil {
			return cleanupAfterErr(ctx, stdout, client, cfg, ns, shape, fmt.Errorf("load generation failed for %s shape: %w", shape, err))
		}
		fmt.Fprintf(stdout, "[%s] load complete\n", shape)
	}

	if cfg.retain {
		fmt.Fprintf(stdout, "[%s] retaining resources (--retain); clean up with: perf cleanup --namespace %s\n\n", shape, ns)
		return nil
	}
	fmt.Fprintf(stdout, "[%s] cleaning up namespace %q ...\n", shape, ns)
	if err := client.Cleanup(ctx, ns); err != nil {
		return fmt.Errorf("cleanup %s: %w", shape, err)
	}
	fmt.Fprintf(stdout, "[%s] done\n\n", shape)
	return nil
}

// exportCredentials returns dummy bearer-token files for the OTEL_COLLECTOR
// provider. The file names must match the launch-spec telemetry Names
// ("perf-logs"/"perf-metrics") the collector config references via ${file:...};
// the sink accepts any token, so the value is irrelevant.
func exportCredentials() map[string]string {
	return map[string]string{
		"perf-logs":    "perf",
		"perf-metrics": "perf",
	}
}

// cleanupAfterErr best-effort cleans up the namespace (unless --retain) and
// returns the original error.
func cleanupAfterErr(ctx context.Context, stdout io.Writer, client *deploy.Client, cfg runConfig, ns string, shape spec.Shape, cause error) error {
	if !cfg.retain {
		if err := client.Cleanup(ctx, ns); err != nil {
			fmt.Fprintf(stdout, "[%s] warning: cleanup after failure did not fully succeed: %v\n", shape, err)
		}
	}
	return cause
}

func runCleanup(stdout io.Writer, cfg cleanupConfig) error {
	shapes, err := shapesFromFlag(cfg.shape)
	if err != nil {
		return err
	}
	client, err := deploy.NewClient(cfg.kubeconfig, cfg.kubeContext)
	if err != nil {
		return err
	}

	ctx := context.Background()
	seen := map[string]bool{}
	for _, shape := range shapes {
		for _, ns := range []string{cfg.namespace, namespaceForShape(cfg.namespace, shape, true)} {
			if seen[ns] {
				continue
			}
			seen[ns] = true
			fmt.Fprintf(stdout, "cleaning up namespace %q ...\n", ns)
			if err := client.Cleanup(ctx, ns); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(stdout, "done")
	return nil
}

// namespaceForShape returns the namespace for a shape. When more than one shape
// is deployed, each gets a suffixed namespace so their collector pods and
// services never collide.
func namespaceForShape(base string, shape spec.Shape, suffix bool) string {
	if !suffix {
		return base
	}
	return fmt.Sprintf("%s-%s", base, shape)
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
