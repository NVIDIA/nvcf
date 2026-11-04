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

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nvcf-cli/internal/clusterdump"
)

var (
	dumpControlPlaneCtx string
	dumpComputePlaneCtx string
	dumpComputeOnly     bool
	dumpOutputFile      string

	// Support-bundle flags. --bundle is the one flag operators need; the rest
	// are advanced knobs with sensible defaults.
	dumpBundle      string
	dumpInclude     []string
	dumpRedact      string
	dumpLogTail     int64
	dumpMaxLogBytes int
)

var clusterDumpCmd = &cobra.Command{
	Use:          "cluster-dump",
	Short:        "Collect a diagnostic snapshot of the self-managed NVCF stack",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	Long: `Collect a diagnostic snapshot of a self-managed NVCF deployment across the
control-plane and compute-plane clusters in one command.

The report consolidates what operators otherwise run by hand: Helm release
versions, node and GPU capacity, pod health, the NVCFBackend custom resource,
recent warning events, and (on the compute plane) ICMSRequest triage, GPU
reconciliation, and any Terminating namespaces. It replaces a sequence of helm
list and kubectl get calls across two kubeconfig contexts and prints to stdout.

Select clusters with --control-plane-context and --compute-plane-context. Omit
--compute-plane-context for a single-cluster deployment. Use --output to write
the report to a file, and --json (global) or a .json output extension for
machine-readable output.

Probe failures (a missing helm binary, an absent CRD, an unreachable namespace)
are reported as per-plane warnings rather than aborting the dump.

For a full support bundle to share with NVIDIA, add --bundle <path>. A path
ending in .tar.gz or .tgz writes a single archive; any other path writes a
directory tree. On top of everything in the report, the bundle adds the raw
artifacts: per-namespace resource manifests, bounded pod logs, and helm
manifest/values. Secret values are masked by default (--redact secrets).`,
	RunE: runClusterDump,
}

func init() {
	rootCmd.AddCommand(clusterDumpCmd)
	clusterDumpCmd.Flags().StringVar(&dumpControlPlaneCtx, "control-plane-context", "",
		"Kubeconfig context for the control-plane cluster (defaults to current context)")
	clusterDumpCmd.Flags().StringVar(&dumpComputePlaneCtx, "compute-plane-context", "",
		"Kubeconfig context for the compute-plane cluster (omit for single-cluster)")
	clusterDumpCmd.Flags().BoolVar(&dumpComputeOnly, "compute-only", false,
		"Collect only the compute plane (skip the control plane); uses --compute-plane-context or the current context")
	clusterDumpCmd.Flags().StringVar(&dumpOutputFile, "output", "",
		"Write the dump to a file instead of stdout (.json extension selects JSON format)")

	clusterDumpCmd.Flags().StringVar(&dumpBundle, "bundle", "",
		"Write a full support bundle to this path (.tar.gz/.tgz = archive, otherwise a directory)")

	// Advanced bundle tuning; defaults are right for most operators.
	clusterDumpCmd.Flags().StringVar(&dumpRedact, "redact", clusterdump.RedactSecrets,
		"Bundle redaction level: secrets (default), none, all")
	clusterDumpCmd.Flags().StringSliceVar(&dumpInclude, "include", nil,
		"Limit which heavy artifacts the bundle writes (advanced): resources,logs,helm (default: all)")
	clusterDumpCmd.Flags().Int64Var(&dumpLogTail, "log-tail", 2000,
		"Bundle: max log lines per container (advanced)")
	clusterDumpCmd.Flags().IntVar(&dumpMaxLogBytes, "max-log-bytes", 1<<20,
		"Bundle: max log bytes per container (advanced)")
}

// includableSections are the heavy bundle artifacts an operator can select via
// --include. The cheap summaries (nodes, GPU, ICMS, stuck namespaces) are always
// collected and are not gated here.
var includableSections = map[string]bool{
	clusterdump.SectionResources: true,
	clusterdump.SectionLogs:      true,
	clusterdump.SectionHelm:      true,
	clusterdump.SectionAll:       true,
}

// dumpOptionsFromFlags builds the package-level DumpOptions from the parsed
// flags. When --bundle is set without an explicit --include, it defaults to the
// full set of sections. An unrecognised --include value is rejected so a typo
// does not silently drop every raw artifact from the bundle.
func dumpOptionsFromFlags() (clusterdump.DumpOptions, error) {
	sections := map[string]bool{}
	for _, s := range dumpInclude {
		s = strings.TrimSpace(strings.ToLower(s))
		if s == "" {
			continue
		}
		if !includableSections[s] {
			return clusterdump.DumpOptions{}, fmt.Errorf(
				"unknown --include section %q; valid values: resources, logs, helm, all", s)
		}
		sections[s] = true
	}
	if dumpBundle != "" && len(sections) == 0 {
		// The cheap summaries (ICMS, GPU, stuck namespaces) are always collected
		// and shown in the report; these sections control the heavier artifacts
		// written into the bundle.
		sections[clusterdump.SectionResources] = true
		sections[clusterdump.SectionLogs] = true
		sections[clusterdump.SectionHelm] = true
	}
	return clusterdump.DumpOptions{
		Sections:    sections,
		Redact:      dumpRedact,
		LogTail:     dumpLogTail,
		MaxLogBytes: dumpMaxLogBytes,
	}, nil
}

// tuneRestConfig raises client-side rate limits so a bundle that lists many
// resources and streams many pod logs is not throttled by the default QPS.
func tuneRestConfig(cfg *rest.Config) {
	if cfg.QPS < 50 {
		cfg.QPS = 50
	}
	if cfg.Burst < 100 {
		cfg.Burst = 100
	}
}

// dumpCollector is the seam the runner depends on so tests can inject a fake
// without building real kube clients. *clusterdump.Collector satisfies it.
type dumpCollector interface {
	Collect(ctx context.Context) (clusterdump.Dump, error)
}

// newDumpCollector is a package-level seam replaced in tests.
var newDumpCollector = func() (dumpCollector, error) {
	return loadDumpCollector()
}

func loadDumpCollector() (*clusterdump.Collector, error) {
	opts, err := dumpOptionsFromFlags()
	if err != nil {
		return nil, err
	}
	c := &clusterdump.Collector{}

	// Control plane (skipped in --compute-only mode).
	if !dumpComputeOnly {
		controlCfg, err := buildRestConfigForCtx(dumpControlPlaneCtx)
		if err != nil {
			return nil, fmt.Errorf("build control-plane rest config: %w", err)
		}
		tuneRestConfig(controlCfg)
		controlKube, err := kubernetes.NewForConfig(controlCfg)
		if err != nil {
			return nil, fmt.Errorf("build control-plane kube client: %w", err)
		}
		// The dynamic client powers the deep per-resource capture on both planes.
		// NVCFBackend collection stays gated on the compute role inside the collector.
		controlDyn, err := dynamic.NewForConfig(controlCfg)
		if err != nil {
			return nil, fmt.Errorf("build control-plane dynamic client: %w", err)
		}
		c.Control = &clusterdump.PlaneCollector{
			Role:       clusterdump.RoleControlPlane,
			Context:    dumpControlPlaneCtx,
			Kube:       controlKube,
			Dynamic:    controlDyn,
			Discovery:  controlKube.Discovery(),
			Helm:       clusterdump.NewExecHelmRunner(),
			Namespaces: clusterdump.ControlPlaneNamespaces(),
			Options:    opts,
		}
	}

	// Compute plane: collected when a compute context is named, or always in
	// --compute-only mode (where it falls back to the current kubeconfig context).
	if dumpComputePlaneCtx != "" || dumpComputeOnly {
		computeCfg, err := buildRestConfigForCtx(dumpComputePlaneCtx)
		if err != nil {
			return nil, fmt.Errorf("build compute-plane rest config: %w", err)
		}
		tuneRestConfig(computeCfg)
		computeKube, err := kubernetes.NewForConfig(computeCfg)
		if err != nil {
			return nil, fmt.Errorf("build compute-plane kube client: %w", err)
		}
		computeDyn, err := dynamic.NewForConfig(computeCfg)
		if err != nil {
			return nil, fmt.Errorf("build compute-plane dynamic client: %w", err)
		}
		c.Compute = &clusterdump.PlaneCollector{
			Role:       clusterdump.RoleComputePlane,
			Context:    dumpComputePlaneCtx,
			Kube:       computeKube,
			Dynamic:    computeDyn,
			Discovery:  computeKube.Discovery(),
			Helm:       clusterdump.NewExecHelmRunner(),
			Namespaces: clusterdump.ComputePlaneNamespaces(),
			Options:    opts,
		}
	}
	return c, nil
}

// buildRestConfigForCtx resolves a *rest.Config for the named kubeconfig
// context. It is the rest.Config-returning dual of buildKubeClientForCtx
// (cmd/self_hosted_status.go); the dump needs the config to build both the
// typed and dynamic clients. An empty context uses the kubeconfig's
// current-context.
func buildRestConfigForCtx(kubeCtx string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

func runClusterDump(cmd *cobra.Command, _ []string) error {
	coll, err := newDumpCollector()
	if err != nil {
		return err
	}
	d, err := coll.Collect(cmd.Context())
	if err != nil {
		return err
	}
	return writeDump(cmd, d)
}

// isArchivePath reports whether a --bundle path should be written as a gzipped
// tar rather than a directory tree.
func isArchivePath(p string) bool {
	return strings.HasSuffix(p, ".tar.gz") || strings.HasSuffix(p, ".tgz")
}

// writeDump renders the dump as text or JSON, to stdout or a file. JSON is
// selected by the global --json flag or a .json output extension.
func writeDump(cmd *cobra.Command, d clusterdump.Dump) error {
	// Bundle mode: a full support bundle as a directory or .tar.gz. The concise
	// summary still prints to stdout for immediate triage. Format is chosen by
	// the path extension.
	if dumpBundle != "" {
		if err := clusterdump.RenderText(cmd.OutOrStdout(), d); err != nil {
			return err
		}
		if isArchivePath(dumpBundle) {
			// 0600: the bundle may contain Secret manifests and operator
			// diagnostics, matching the directory renderer's per-file mode.
			f, err := os.OpenFile(dumpBundle, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return fmt.Errorf("create bundle archive: %w", err)
			}
			defer f.Close()
			if err := clusterdump.RenderArchive(f, d); err != nil {
				return fmt.Errorf("write bundle archive: %w", err)
			}
		} else if err := clusterdump.RenderDirectory(dumpBundle, d); err != nil {
			return fmt.Errorf("write bundle directory: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "\nDiagnostic bundle written to %s\n", dumpBundle)
		return nil
	}

	useJSON := IsJSONOutput() || strings.HasSuffix(dumpOutputFile, ".json")

	w := cmd.OutOrStdout()
	if dumpOutputFile != "" {
		// 0600: the report (JSON especially) carries cluster topology; keep it
		// owner-only like the bundle.
		f, err := os.OpenFile(dumpOutputFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer f.Close()
		w = f
	}

	if useJSON {
		return clusterdump.RenderJSON(w, d)
	}
	return clusterdump.RenderText(w, d)
}
