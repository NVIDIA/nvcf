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

// Command perf is the entrypoint for the BYOO collector performance suite:
// "render" validates the workload with no cluster, "run" deploys the collector
// and waits for readiness, and "cleanup" removes suite-created resources. Load
// generation and measurement land in a later milestone.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"

	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/deploy"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/k3d"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/loadgen"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/profile"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/render"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/report"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/sink"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/spec"
	"github.com/NVIDIA/nvcf/tests/perf/byoo-otel-collector/pkg/validate"
)

// newDeployClient constructs the cluster client. It is a package variable so
// tests can inject a fake-backed client.
var newDeployClient = deploy.NewClient

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
	startupTarget  time.Duration
	startupMax     time.Duration
	retain         bool
	skipLoad       bool
	k3dCluster     string
	importImages   bool
	resultsDir     string
}

func newRunCmd() *cobra.Command {
	var cfg runConfig
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Deploy the collector + OTLP sink, check startup health, and drive load",
		Long: `run renders the production workload shape via icms-translate, validates it,
deploys an in-cluster OTLP sink, deploys the authentic BYOO collector pointed at
that sink, measures collector startup health, waits for both to become ready,
and drives telemetrygen load at the selected profile's rates. It cleans up
afterward unless --retain is set.

With --mode k3d (the default) it provisions a dedicated local k3d cluster, runs
against it, and deletes it afterward (unless --retain). With --mode remote it
uses the ambient kubeconfig (or --kubeconfig/--context). Measurement and
reporting land in a later milestone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd.OutOrStdout(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.shape, "shape", "both", `deployment shape: "container", "helm", or "both"`)
	cmd.Flags().StringVar(&cfg.profile, "profile", "dev", `execution profile: "dev" or "baseline"`)
	cmd.Flags().StringVar(&cfg.mode, "mode", "k3d", `deployment mode: "k3d" (managed local cluster) or "remote" (ambient kubeconfig)`)
	cmd.Flags().StringVar(&cfg.collectorImage, "collector-image", spec.DefaultCollectorImage, "BYOO collector image reference")
	cmd.Flags().StringVar(&cfg.sinkImage, "sink-image", sink.DefaultImage, "OTLP sink (collector-contrib) image reference")
	cmd.Flags().StringVar(&cfg.loadgenImage, "loadgen-image", loadgen.DefaultImage, "telemetrygen load generator image reference")
	cmd.Flags().StringVar(&cfg.namespace, "namespace", "byoo-perf", "base namespace for deployed resources")
	cmd.Flags().StringVar(&cfg.kubeconfig, "kubeconfig", "", "path to kubeconfig (remote mode; defaults to in-cluster or $KUBECONFIG)")
	cmd.Flags().StringVar(&cfg.kubeContext, "context", "", "kubeconfig context to use (remote mode)")
	cmd.Flags().DurationVar(&cfg.readyTimeout, "ready-timeout", 3*time.Minute, "how long to wait for the collector and sink to become ready")
	cmd.Flags().DurationVar(&cfg.startupTarget, "startup-target", 15*time.Second, "target collector-container start-to-health duration")
	cmd.Flags().DurationVar(&cfg.startupMax, "startup-max", 30*time.Second, "maximum collector-container start-to-health duration")
	cmd.Flags().BoolVar(&cfg.retain, "retain", false, "retain deployed resources (and the managed k3d cluster) instead of cleaning up")
	cmd.Flags().BoolVar(&cfg.skipLoad, "skip-load", false, "deploy the collector and sink but do not drive load")
	cmd.Flags().StringVar(&cfg.k3dCluster, "k3d-cluster", "byoo-perf", "name of the managed k3d cluster (k3d mode)")
	cmd.Flags().BoolVar(&cfg.importImages, "import-images", false, "import the collector/sink/loadgen images from local Docker into the k3d cluster (k3d mode)")
	cmd.Flags().StringVar(&cfg.resultsDir, "results-dir", "", "directory to write structured JSON results to (one file per shape and repetition)")
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
	if err := validateStartupThresholds(cfg.startupTarget, cfg.startupMax); err != nil {
		return err
	}
	prof, err := profile.Lookup(cfg.profile)
	if err != nil {
		return err
	}
	shapes, err := shapesFromFlag(cfg.shape)
	if err != nil {
		return err
	}

	ctx := context.Background()

	// In managed k3d mode the suite owns the cluster lifecycle: provision it
	// up front and tear it down at the end (unless --retain). In remote mode it
	// uses the ambient kubeconfig/context.
	kubeconfig, kubeContext := cfg.kubeconfig, cfg.kubeContext
	if cfg.mode == "k3d" {
		cluster, teardown, err := ensureK3dCluster(ctx, stdout, cfg)
		if err != nil {
			return err
		}
		defer teardown()
		kubeconfig, kubeContext = "", cluster.Context
	}

	client, err := newDeployClient(kubeconfig, kubeContext)
	if err != nil {
		return err
	}

	loadDuration := loadGenDuration(prof)
	fmt.Fprintf(stdout, "mode=%s profile=%s warmup=%s window=%s reps=%d\n\n", cfg.mode, prof.Name, prof.Warmup, prof.MeasurementWindow, prof.Repetitions)

	multi := len(shapes) > 1
	for _, shape := range shapes {
		if err := runShape(ctx, stdout, client, cfg, prof, shape, multi, loadDuration); err != nil {
			return err
		}
	}

	if cfg.skipLoad {
		fmt.Fprintln(stdout, "note: --skip-load set; the collector and sink were deployed but no load was driven and no baseline was measured.")
	} else {
		fmt.Fprintln(stdout, "note: load was driven end-to-end and a baseline was measured. Startup health has a pass/fail bound; throughput and delivery numbers establish the reproducible baseline.")
	}
	return nil
}

// ensureK3dCluster provisions (or reuses) the managed k3d cluster and returns a
// teardown function that deletes it after the run unless --retain is set.
func ensureK3dCluster(ctx context.Context, stdout io.Writer, cfg runConfig) (*k3d.Cluster, func(), error) {
	if err := k3d.EnsureInstalled(ctx); err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(stdout, "provisioning managed k3d cluster %q ...\n", cfg.k3dCluster)
	cluster, err := k3d.Create(ctx, k3d.DefaultOptions(cfg.k3dCluster))
	if err != nil {
		return nil, nil, err
	}
	if cluster.Reused {
		fmt.Fprintf(stdout, "reusing pre-existing k3d cluster %q; it will be left in place\n", cfg.k3dCluster)
	}
	if cfg.importImages {
		images := []string{cfg.collectorImage, cfg.sinkImage, cfg.loadgenImage}
		fmt.Fprintf(stdout, "importing images into k3d cluster %q: %s\n", cfg.k3dCluster, strings.Join(images, ", "))
		if err := k3d.ImportImages(ctx, cluster.Name, images...); err != nil {
			// Only delete a cluster this run created; never tear down a
			// pre-existing one the developer owns.
			if !cfg.retain && !cluster.Reused {
				_ = k3d.Delete(ctx, cfg.k3dCluster)
			}
			return nil, nil, err
		}
	}
	fmt.Fprintf(stdout, "using kube context %q\n\n", cluster.Context)

	teardown := func() {
		if cluster.Reused {
			fmt.Fprintf(stdout, "reused pre-existing k3d cluster %q; leaving it in place\n", cfg.k3dCluster)
			return
		}
		if cfg.retain {
			fmt.Fprintf(stdout, "retaining managed k3d cluster %q (--retain); delete with: k3d cluster delete %s\n", cfg.k3dCluster, cfg.k3dCluster)
			return
		}
		fmt.Fprintf(stdout, "deleting managed k3d cluster %q ...\n", cfg.k3dCluster)
		if err := k3d.Delete(ctx, cfg.k3dCluster); err != nil {
			fmt.Fprintf(stdout, "warning: failed to delete k3d cluster %q: %v\n", cfg.k3dCluster, err)
		}
	}
	return cluster, teardown, nil
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
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("deploy sink for %s: %w", shape, err))
	}
	if err := client.WaitPodReady(ctx, ns, sinkDep.PodName, cfg.readyTimeout); err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("sink did not become ready for %s shape: %w", shape, err))
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
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("render %s: %w", shape, err))
	}
	exp := validate.Expectations{
		Image:     opts.CollectorImage,
		Resources: common.GetDefaultContainerResourcesBYOO(),
	}
	if err := validate.Render(res, exp); err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("validate %s: %w", shape, err))
	}

	fmt.Fprintf(stdout, "[%s] deploying collector to namespace %q ...\n", shape, ns)
	dep, err := client.Deploy(ctx, ns, res, deploy.WithExportCredentials(exportCredentials()))
	if err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("deploy %s: %w", shape, err))
	}

	fmt.Fprintf(stdout, "[%s] waiting up to %s for collector pod %q to report healthy on /health ...\n", shape, cfg.readyTimeout, dep.PodName)
	startup, err := client.WaitCollectorHealth(ctx, ns, dep.PodName, render.CollectorContainerName, cfg.readyTimeout)
	if err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("collector did not report healthy for %s shape: %w", shape, err))
	}
	printStartupHealth(stdout, shape, startup, cfg.startupTarget, cfg.startupMax)
	if err := checkStartupHealth(startup, cfg.startupMax); err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("collector startup failed for %s shape: %w", shape, err))
	}

	fmt.Fprintf(stdout, "[%s] waiting up to %s for collector pod %q to become ready ...\n", shape, cfg.readyTimeout, dep.PodName)
	if err := client.WaitPodReady(ctx, ns, dep.PodName, cfg.readyTimeout); err != nil {
		return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("collector did not become ready for %s shape: %w", shape, err))
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

	// 3. Drive load through the collector and measure over the window.
	if !cfg.skipLoad {
		grpcEndpoint := dep.Endpoints["otlp-grpc"]
		if grpcEndpoint == "" {
			return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("collector has no otlp-grpc endpoint for %s shape", shape))
		}
		collectorMetricsPort, ok := containerPortByName(res.Collector, "metrics")
		if !ok {
			return cleanupAfterErr(ctx, client, cfg, ns, fmt.Errorf("collector container exposes no %q port for %s shape", "metrics", shape))
		}
		lgOpts := loadgen.Options{
			Image:         cfg.loadgenImage,
			Endpoint:      grpcEndpoint,
			Insecure:      true,
			Duration:      loadDuration,
			LogsPerSec:    prof.LogRecordsPerSec,
			MetricsPerSec: prof.MetricDataPointsPerSec,
		}
		// Execute one load+measure cycle per repetition so a "baseline" run
		// honors the profile's repetition count instead of collapsing to a
		// single sample. A generator failure marks only that run invalid.
		reports, err := runRepetitions(stdout, prof, shape,
			func(run int) error {
				jobs := loadgen.Jobs(ns, dep.PodName, lgOpts)
				fmt.Fprintf(stdout, "[%s] driving load for %s (logs=%d/s metrics=%d/s) ...\n", shape, loadDuration, lgOpts.LogsPerSec, lgOpts.MetricsPerSec)
				if err := client.StartLoad(ctx, ns, jobs); err != nil {
					return fmt.Errorf("start load for %s shape (run %d): %w", shape, run, err)
				}
				// Wait for the generators to actually start before the
				// measurement warmup so scheduling and image-pull latency do
				// not consume part of the window.
				if err := client.WaitLoadStarted(ctx, ns, jobs, cfg.readyTimeout); err != nil {
					return fmt.Errorf("wait for load generators to start for %s shape (run %d): %w", shape, run, err)
				}
				return nil
			},
			func(run int) report.ShapeReport {
				return measure(ctx, stdout, client, cfg, prof, shape, ns, dep.PodName, collectorMetricsPort, startup)
			},
			func(run int) error {
				return client.WaitLoad(ctx, ns, loadgen.Jobs(ns, dep.PodName, lgOpts), cfg.readyTimeout)
			},
		)
		if err != nil {
			return cleanupAfterErr(ctx, client, cfg, ns, err)
		}

		for _, rep := range reports {
			rep.WriteSummary(stdout)
			if err := writeReport(cfg.resultsDir, shape, rep); err != nil {
				fmt.Fprintf(stdout, "[%s] warning: could not persist results: %v\n", shape, err)
			} else if cfg.resultsDir != "" {
				fmt.Fprintf(stdout, "[%s] results written to %s\n", shape, filepath.Join(cfg.resultsDir, resultFileName(shape, rep)))
			}
		}
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

// measure samples the collector and sink metric endpoints across the profile's
// measurement window (after a warmup) and computes the baseline. Scrapes are
// best-effort: a failed scrape yields empty samples, which Build records as
// missing rather than failing the run.
func measure(ctx context.Context, stdout io.Writer, client *deploy.Client, cfg runConfig, prof profile.Profile, shape spec.Shape, ns, collectorPod, collectorMetricsPort string, startup report.StartupHealth) report.ShapeReport {
	snap := func(label string) report.Snapshot {
		s, collErr, sinkErr := takeSnapshot(
			func() (report.Samples, error) {
				return scrapeSamples(ctx, client, ns, collectorPod, collectorMetricsPort)
			},
			func() (report.Samples, error) {
				return scrapeSamples(ctx, client, ns, sink.Name, strconv.Itoa(sink.MetricsPort))
			},
		)
		if collErr != nil {
			fmt.Fprintf(stdout, "[%s] warning: %s collector scrape failed: %v\n", shape, label, collErr)
		}
		if sinkErr != nil {
			fmt.Fprintf(stdout, "[%s] warning: %s sink scrape failed: %v\n", shape, label, sinkErr)
		}
		return s
	}

	fmt.Fprintf(stdout, "[%s] warmup %s ...\n", shape, prof.Warmup)
	sleep(ctx, prof.Warmup)
	start := snap("start")
	fmt.Fprintf(stdout, "[%s] measuring for %s ...\n", shape, prof.MeasurementWindow)
	sleep(ctx, prof.MeasurementWindow)
	end := snap("end")

	health, healthErr := client.PodHealth(ctx, ns, collectorPod)
	if healthErr != nil {
		fmt.Fprintf(stdout, "[%s] warning: could not read pod health: %v\n", shape, healthErr)
	}

	return report.Build(report.Inputs{
		Shape:         string(shape),
		Profile:       prof.Name,
		LogsPerSec:    prof.LogRecordsPerSec,
		MetricsPerSec: prof.MetricDataPointsPerSec,
		Window:        report.Window{Start: start, End: end},
		Health:        health,
		HealthErr:     healthErr,
		StartupHealth: &startup,
	})
}

func validateStartupThresholds(target, max time.Duration) error {
	if target <= 0 {
		return fmt.Errorf("startup target must be positive, got %s", target)
	}
	if max < target {
		return fmt.Errorf("startup maximum %s must be at least the target %s", max, target)
	}
	return nil
}

func printStartupHealth(w io.Writer, shape spec.Shape, startup report.StartupHealth, target, max time.Duration) {
	podToHealth := time.Duration(startup.PodToHealthSeconds * float64(time.Second)).Round(time.Millisecond)
	collectorToHealth := time.Duration(startup.CollectorToHealthSeconds * float64(time.Second)).Round(time.Millisecond)
	fmt.Fprintf(w, "[%s] startup health: pod_to_health=%s collector_to_health=%s (target <= %s, maximum <= %s)\n", shape, podToHealth, collectorToHealth, target, max)
	if collectorToHealth > target && collectorToHealth <= max {
		fmt.Fprintf(w, "[%s] warning: collector startup exceeded the %s target\n", shape, target)
	}
}

func checkStartupHealth(startup report.StartupHealth, max time.Duration) error {
	collectorToHealth := time.Duration(startup.CollectorToHealthSeconds * float64(time.Second))
	if collectorToHealth > max {
		return fmt.Errorf("collector start-to-health duration %s exceeded the %s maximum", collectorToHealth.Round(time.Millisecond), max)
	}
	return nil
}

// loadStartupMargin extends the generator run beyond warmup+window. The
// generators start when their pods reach Running; measurement warmup only
// begins after that, so without a margin the measurement window would extend
// past the end of load generation and read a load-free tail (low throughput /
// delivery). The margin absorbs the residual startup and warmup jitter so the
// generators are still running when the window closes. It is a variable so
// tests can adjust it.
var loadStartupMargin = 30 * time.Second

// loadGenDuration is how long the telemetrygen Jobs run: the full warmup plus
// measurement window plus a startup margin, so the measurement window always
// closes while load is still in flight.
func loadGenDuration(prof profile.Profile) time.Duration {
	return prof.Warmup + prof.MeasurementWindow + loadStartupMargin
}

// takeSnapshot scrapes the collector and sink concurrently and stamps
// Snapshot.At only after both responses return, so a slow scrape cannot skew
// the measurement window: the timestamp reflects when the samples were taken,
// not when scraping began. Scrape errors are returned (not fatal) so the caller
// can log them and Build can record the missing series.
func takeSnapshot(scrapeCollector, scrapeSink func() (report.Samples, error)) (snap report.Snapshot, collErr, sinkErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s, err := scrapeCollector()
		if err != nil {
			collErr = err
			return
		}
		snap.Collector = s
	}()
	go func() {
		defer wg.Done()
		s, err := scrapeSink()
		if err != nil {
			sinkErr = err
			return
		}
		snap.Sink = s
	}()
	wg.Wait()
	snap.At = time.Now()
	return snap, collErr, sinkErr
}

// scrapeSamples fetches and parses a pod's Prometheus metrics endpoint.
func scrapeSamples(ctx context.Context, client *deploy.Client, ns, pod, port string) (report.Samples, error) {
	raw, err := client.ScrapePodMetrics(ctx, ns, pod, port, "/metrics")
	if err != nil {
		return report.Samples{}, err
	}
	return report.Parse(string(raw)), nil
}

// runRepetitions executes prof.Repetitions load+measure cycles, tagging each
// resulting report with its run index. A startLoad error aborts and is returned
// so the caller can clean up; a waitLoad error marks that single run invalid
// (preserving whatever partial data was measured) but does not abort the rest.
func runRepetitions(
	stdout io.Writer,
	prof profile.Profile,
	shape spec.Shape,
	startLoad func(run int) error,
	measureOnce func(run int) report.ShapeReport,
	waitLoad func(run int) error,
) ([]report.ShapeReport, error) {
	reps := prof.Repetitions
	if reps < 1 {
		reps = 1
	}
	reports := make([]report.ShapeReport, 0, reps)
	for run := 1; run <= reps; run++ {
		if reps > 1 {
			fmt.Fprintf(stdout, "[%s] repetition %d/%d\n", shape, run, reps)
		}
		if err := startLoad(run); err != nil {
			return reports, err
		}
		rep := measureOnce(run)
		rep.Run = run
		rep.Repetitions = reps
		if err := waitLoad(run); err != nil {
			rep.MarkInvalid(fmt.Sprintf("load generators did not complete cleanly: %v", err))
			fmt.Fprintf(stdout, "[%s] run %d/%d warning: %v\n", shape, run, reps, err)
		} else {
			fmt.Fprintf(stdout, "[%s] run %d/%d load complete\n", shape, run, reps)
		}
		reports = append(reports, rep)
	}
	return reports, nil
}

// resultFileName is the per-run result file: <shape>.json for a single
// repetition, <shape>-run<N>.json when a profile requests several so each run
// is preserved as a distinct record.
func resultFileName(shape spec.Shape, rep report.ShapeReport) string {
	if rep.Repetitions > 1 {
		return fmt.Sprintf("%s-run%d.json", shape, rep.Run)
	}
	return string(shape) + ".json"
}

// writeReport persists the report under dir (per resultFileName) when dir is set.
func writeReport(dir string, shape spec.Shape, rep report.ShapeReport) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := rep.JSON()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, resultFileName(shape, rep)), data, 0o644)
}

// containerPortByName returns the numeric container port with the given name as
// a string, for API-proxy scraping.
func containerPortByName(c corev1.Container, name string) (string, bool) {
	for _, p := range c.Ports {
		if p.Name == name {
			return strconv.Itoa(int(p.ContainerPort)), true
		}
	}
	return "", false
}

// sleep waits for d or until the context is cancelled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// exportCredentials returns the dummy accounts-secrets entries for the
// OTEL_COLLECTOR provider. Keys must match the launch-spec telemetry Names
// ("perf-logs"/"perf-metrics"); the collector's secrets-extractor turns each
// into a token file the exporter config references via ${file:...}. The sink
// accepts any token, so the values are irrelevant.
func exportCredentials() map[string]string {
	return map[string]string{
		"perf-logs":    "perf",
		"perf-metrics": "perf",
	}
}

// cleanupAfterErr cleans up the namespace (unless --retain) and returns the
// original failure joined with any cleanup error, so a cleanup failure is
// surfaced rather than silently discarded.
func cleanupAfterErr(ctx context.Context, client *deploy.Client, cfg runConfig, ns string, cause error) error {
	return errors.Join(cause, cleanupOnFailure(ctx, client, ns, cfg.retain))
}

func runCleanup(stdout io.Writer, cfg cleanupConfig) error {
	shapes, err := shapesFromFlag(cfg.shape)
	if err != nil {
		return err
	}
	client, err := newDeployClient(cfg.kubeconfig, cfg.kubeContext)
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

// cleanupOnFailure removes suite resources after a failed run step unless
// --retain is set, returning any cleanup error so callers can join it with the
// original failure instead of silently discarding it.
func cleanupOnFailure(ctx context.Context, client *deploy.Client, namespace string, retain bool) error {
	if retain {
		return nil
	}
	if err := client.Cleanup(ctx, namespace); err != nil {
		return fmt.Errorf("cleanup namespace %q after failure: %w", namespace, err)
	}
	return nil
}

// namespaceForShape suffixes the namespace per shape when multiple are deployed
// so their pods and services never collide.
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

	// Diagnostics go to stderr so stdout stays machine-readable in yaml/json.
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

// printYAML writes the bench pods as a multi-document stream so --shape both
// stays a valid manifest; the per-shape note goes to stderr to keep stdout
// parseable.
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

// printJSON writes the bench pods as a JSON array so multiple shapes emit one
// valid document.
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
