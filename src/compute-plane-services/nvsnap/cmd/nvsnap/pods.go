/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

// `nvsnap pods` subcommand: list pods + show cold-start vs NvSnap-restored
// timings. Reads K8s directly (no nvsnap-server dependency) so it works
// even when nvsnap-server is unreachable.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/podtimings"
)

func podsCmd() *cobra.Command {
	var (
		podsNamespace string
		podsAllNS     bool
		podsKubeCfg   string
		podsWatch     bool
		podsShowNode  bool
		podsLabelSel  string
	)

	cmd := &cobra.Command{
		Use:   "pods [namespace]",
		Short: "Show pod lifecycle timings (cold-start vs NvSnap-restored)",
		Long: `Show pod lifecycle timings — cold-start vs NvSnap-restored.

Classifies each pod by the presence of the nvsnap.io/restore-from
annotation NVCA's Hook A stamps on pods whose function-version has
a usable checkpoint cached locally. Reports ready-in time per pod
plus an aggregate "NvSnap saved you X" summary at the bottom.

Examples:
  # Pods in the current namespace
  nvsnap pods

  # Pods in a specific namespace
  nvsnap pods nvcf-backend
  nvsnap pods -n nvcf-backend

  # All namespaces
  nvsnap pods -A

  # Machine-readable
  nvsnap pods -A --json

  # Filter by label
  nvsnap pods -A -l function-version-id=cd1116dc-...

  # Show landing node (useful for cross-node cascade-fetch debugging)
  nvsnap pods -A --show-node`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				podsNamespace = args[0]
			}

			client, ns, err := newKubeClient(podsKubeCfg, podsNamespace, podsAllNS)
			if err != nil {
				return fmt.Errorf("kube client: %w", err)
			}

			if podsWatch {
				return runPodsWatch(cmd.Context(), client, ns, podsLabelSel, podsShowNode)
			}
			return runPodsOnce(cmd.Context(), client, ns, podsLabelSel, podsShowNode)
		},
	}

	cmd.Flags().StringVarP(&podsNamespace, "namespace", "n", "", "Namespace to query (default: current context's namespace)")
	cmd.Flags().BoolVarP(&podsAllNS, "all-namespaces", "A", false, "List pods across all namespaces")
	cmd.Flags().StringVar(&podsKubeCfg, "kubeconfig", "", "Path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	cmd.Flags().BoolVarP(&podsWatch, "watch", "w", false, "Re-render every 2s until interrupted (Ctrl-C)")
	cmd.Flags().BoolVar(&podsShowNode, "show-node", false, "Add the NODE column to the table")
	cmd.Flags().StringVarP(&podsLabelSel, "selector", "l", "", "Label selector (same syntax as kubectl -l)")

	return cmd
}

// newKubeClient builds a clientset using:
//   - explicit --kubeconfig flag if set,
//   - else $KUBECONFIG,
//   - else ~/.kube/config (clientcmd's default).
//
// Namespace resolution: --all-namespaces wins, else -n, else
// kubeconfig's current-context namespace, else "default".
func newKubeClient(kubecfg, ns string, allNS bool) (kubernetes.Interface, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubecfg != "" {
		loadingRules.ExplicitPath = kubecfg
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, "", err
	}

	if allNS {
		return clientset, "", nil
	}
	if ns != "" {
		return clientset, ns, nil
	}
	// Resolve namespace from kubeconfig context.
	if defaultNS, _, err := cc.Namespace(); err == nil && defaultNS != "" {
		return clientset, defaultNS, nil
	}
	return clientset, "default", nil
}

func runPodsOnce(ctx context.Context, client kubernetes.Interface, ns, sel string, showNode bool) error {
	pods, err := listPods(ctx, client, ns, sel)
	if err != nil {
		return err
	}
	return render(pods, ns, showNode)
}

func runPodsWatch(ctx context.Context, client kubernetes.Interface, ns, sel string, showNode bool) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pods, err := listPods(ctx, client, ns, sel)
		if err != nil {
			return err
		}
		// ANSI clear-screen + home cursor. Falls back gracefully on
		// terminals that don't honor it (output is still correct,
		// just doesn't refresh in place).
		fmt.Print("\033[2J\033[H")
		fmt.Printf("nvsnap pods — %s — refreshing every 2s (Ctrl-C to exit)\n\n",
			time.Now().Format(time.RFC3339))
		if err := render(pods, ns, showNode); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func listPods(ctx context.Context, client kubernetes.Interface, ns, selector string) ([]corev1.Pod, error) {
	opts := metav1.ListOptions{LabelSelector: selector}
	list, err := client.CoreV1().Pods(ns).List(ctx, opts)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func render(pods []corev1.Pod, ns string, showNode bool) error {
	timings := podtimings.Compute(pods)
	if outputJSON {
		return podtimings.RenderJSON(os.Stdout, timings)
	}
	return podtimings.RenderTable(os.Stdout, timings, podtimings.RenderOptions{
		ShowNamespace: ns == "", // all-namespaces case
		ShowNode:      showNode,
	})
}
