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
	"io"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/progress"
	"nvcf-cli/internal/selfhosted/status"
)

var (
	statusWatch         bool
	statusWatchInterval time.Duration
	statusComponent     string
	statusNoEvents      bool
	statusClusterName   string
	statusNCAID         string
)

var selfHostedStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Show steady-state status of the self-hosted NVCF deployment",
	SilenceUsage: true,
	RunE:         runSelfHostedStatus,
}

func init() {
	selfHostedCmd.AddCommand(selfHostedStatusCmd)
	selfHostedStatusCmd.Flags().BoolVar(&statusWatch, "watch", false,
		"Re-render every --watch-interval until interrupted")
	selfHostedStatusCmd.Flags().DurationVar(&statusWatchInterval, "watch-interval", 5*time.Second,
		"Interval between snapshots in --watch mode")
	selfHostedStatusCmd.Flags().StringVar(&statusComponent, "component", "",
		"Show only the row matching this component name (reserved; not yet filtered)")
	selfHostedStatusCmd.Flags().BoolVar(&statusNoEvents, "no-events", false,
		"Drop the recent-events panel (reserved; no events collected yet)")
	selfHostedStatusCmd.Flags().StringVar(&statusClusterName, "cluster-name", "",
		"Local cluster name (defaults to CLUSTER_NAME env var)")
	selfHostedStatusCmd.Flags().StringVar(&statusNCAID, "nca-id", "",
		"NCA ID / account (defaults to NCA_ID env var, then \"nvcf-default\")")
}

// StatusCollector is the interface the cobra runner uses so tests can inject a fake.
type StatusCollector interface {
	Collect(ctx context.Context, sink progress.EventSink) error
}

// newStatusCollector is a package-level seam that tests can replace to inject a
// fake collector without starting real kube / SIS connections.
//
// In split-cluster mode (both --control-plane-context and --compute-plane-context
// are set) two separate kube clients are constructed and handed to the Collector.
// In single-cluster mode only ControlPlaneKube is populated.
var newStatusCollector = func(clusterName, ncaID, icmsURL string) (StatusCollector, error) {
	controlKube, err := buildKubeClientForCtx(selfHostedControlPlaneContext)
	if err != nil {
		return nil, fmt.Errorf("build control-plane kube client: %w", err)
	}

	var computeKube kubernetes.Interface
	if selfHostedComputePlaneContext != "" {
		computeKube, err = buildKubeClientForCtx(selfHostedComputePlaneContext)
		if err != nil {
			return nil, fmt.Errorf("build compute-plane kube client: %w", err)
		}
	}

	// *client.Client satisfies status.ClusterLister directly via its ListClusters method.
	cfg, err := client.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("load client config: %w", err)
	}
	sisClient, err := client.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("build SIS client: %w", err)
	}

	return &status.Collector{
		ControlPlaneKube:    controlKube,
		ComputePlaneKube:    computeKube,
		ComputePlaneContext: selfHostedComputePlaneContext,
		SIS:                 sisClient,
		SISURL:              icmsURL,
		NCAID:               ncaID,
		Cluster:             clusterName,
		Components:          status.DefaultComponents(),
	}, nil
}

// buildKubeClientForCtx constructs a kubernetes.Interface for the given
// kubeconfig context. When ctx is empty the current-context in kubeconfig is
// used (single-cluster / legacy behaviour).
func buildKubeClientForCtx(ctx string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if ctx != "" {
		overrides.CurrentContext = ctx
	}
	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restCfg)
}

func runSelfHostedStatus(cobraCmd *cobra.Command, _ []string) error {
	ctx := cobraCmd.Context()
	out := cobraCmd.ErrOrStderr()

	// In TTY mode the bubbletea renderer takes over stderr; any rogue
	// log.Printf from the SIS client (e.g. multi_token_transport's
	// "DEBUG: Using API KEY..." per-request line) corrupts the screen.
	// Silence the stdlib default logger for the duration of this command.
	// JSON / plain modes don't have this problem because they emit one
	// line per event and don't redraw. (Iter #9 from dev-VM E2E.)
	if !selfHostedJSON && !selfHostedPlain && !selfHostedAccessible {
		prev := log.Writer()
		log.SetOutput(io.Discard)
		defer log.SetOutput(prev)
	}

	// Resolve cluster name and NCA ID from flags → env → defaults.
	clusterName := statusClusterName
	if clusterName == "" {
		clusterName = os.Getenv("CLUSTER_NAME")
	}

	ncaID := statusNCAID
	if ncaID == "" {
		ncaID = os.Getenv("NCA_ID")
	}
	if ncaID == "" {
		ncaID = "nvcf-default"
	}

	icmsURL := resolveICMSURL(selfHostedICMSURL)

	coll, err := newStatusCollector(clusterName, ncaID, icmsURL)
	if err != nil {
		return err
	}

	sink := selectStatusRenderer(out, statusWatch)
	return runStatusLoop(ctx, coll, sink, statusWatch, statusWatchInterval)
}

// runStatusLoop executes the collect-render loop. Extracted for testability.
// It owns sink.Close() and the optional Start() call.
func runStatusLoop(ctx context.Context, coll StatusCollector, sink progress.EventSink, watch bool, interval time.Duration) error {
	defer func() { _ = sink.Close() }()
	if starter, ok := sink.(interface{ Start() }); ok {
		starter.Start()
	}

	if !watch {
		if err := coll.Collect(ctx, sink); err != nil {
			return err
		}
		// Emit terminal Final so JSONL composers flush the composed snapshot.
		return sink.Emit(ctx, progress.Final{Success: true})
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := coll.Collect(ctx, sink); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			_ = sink.Emit(context.Background(), progress.Final{Cancelled: true})
			return nil
		case <-ticker.C:
		}
	}
}

// selectStatusRenderer picks an EventSink for the status command.
//
// Key differences from SelectRenderer used by `up`:
//   - --json uses NewJSONLRendererForStatus (Plan Deviation #19 compose
//     mode) which buffers ComponentHealth/ClusterRow/RecentEvent events
//     and flushes as a single fat snapshot per §6.5.4.
//   - For one-shot TTY (`status` without --watch), use the static one-shot
//     renderer instead of the bubbletea TTY renderer. Bubbletea's
//     alt-screen restores the terminal on exit, leaving nothing visible
//     once the single Final fires; one-shot renders inline so the user
//     can scroll back. --watch keeps the alt-screen redraw because the
//     dashboard re-renders every interval.
//
// All other format choices delegate to SelectRenderer.
func selectStatusRenderer(w io.Writer, watch bool) progress.EventSink {
	if selfHostedJSON {
		return progress.NewJSONLRendererForStatus(w)
	}
	// One-shot TTY: bubbletea would clear the screen on exit. Use the
	// static one-shot renderer when neither --plain nor --accessible is
	// forcing a stream renderer and stderr is a real TTY.
	if !watch && !selfHostedPlain && !selfHostedAccessible && isWriterTTY(w) {
		return progress.NewStatusOneShotRenderer(w, progress.ModelOpts{
			Mode:                progress.ModeStatus,
			Cluster:             statusClusterName,
			ControlPlaneContext: selfHostedControlPlaneContext,
			ComputePlaneContext: selfHostedComputePlaneContext,
			Output:              w,
		})
	}
	sink, _, _ := progress.SelectRenderer(w, progress.RenderOpts{
		Plain:      selfHostedPlain,
		Accessible: selfHostedAccessible,
		// Without Mode=ModeStatus the bubbletea Model defaults to ModeInstall
		// and renders the 8-phase install dashboard for `status --watch`.
		// (Iter #9 from dev-VM E2E.)
		Mode:                progress.ModeStatus,
		Cluster:             statusClusterName,
		ControlPlaneContext: selfHostedControlPlaneContext,
		ComputePlaneContext: selfHostedComputePlaneContext,
	})
	return sink
}

// isWriterTTY mirrors the helper in internal/selfhosted/progress/select.go
// without exporting it. Returns true iff w is an *os.File whose fd is a
// terminal. Used to pick the one-shot renderer only when we'd otherwise
// have rendered bubbletea (i.e. real TTY output).
func isWriterTTY(w io.Writer) bool {
	type fdGetter interface{ Fd() uintptr }
	f, ok := w.(fdGetter)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
