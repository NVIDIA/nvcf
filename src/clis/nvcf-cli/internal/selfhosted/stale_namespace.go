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

package selfhosted

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// nvcfControlPlaneNamespaces is the canonical set of namespaces created on the
// control-plane cluster by the NVCF self-managed stack. Any of these that
// exist without an active Helm release, or that are stuck Terminating, are
// leftover from a failed or partial teardown.
var nvcfControlPlaneNamespaces = []string{
	"cassandra-system", "nats-system", "nvcf", "api-keys", "ess", "sis",
	"vault-system", "nvcf-backend", "envoy-gateway-system", "openbao-system",
}

// nvcfComputePlaneNamespaces is the canonical set of namespaces created on the
// compute-plane cluster by the NVCF self-managed stack.
var nvcfComputePlaneNamespaces = []string{
	"nvca-operator", "nvca-system",
}

// StaleNamespace describes a single NVCF stack namespace that appears to be a
// leftover from a failed or partial teardown.
type StaleNamespace struct {
	Name   string
	Reason string // human-readable cause: "stuck Terminating" or "no Helm release"
}

// StaleNamespaceProber inspects the given namespaces and returns those that
// appear stale. The probe is read-only; it never deletes or modifies anything.
// A non-nil error means the cluster could not be contacted; the returned slice
// may be a partial result.
type StaleNamespaceProber func(ctx context.Context, kubeContext string, namespaces []string) ([]StaleNamespace, error)

// NewStaleNamespaceProber returns a StaleNamespaceProber backed by client-go.
// Chart-independent: works before any Helm release exists, and uses the
// operator's kubeconfig context to talk to the target cluster.
func NewStaleNamespaceProber() StaleNamespaceProber {
	return func(ctx context.Context, kubeContext string, namespaces []string) ([]StaleNamespace, error) {
		restCfg, err := loadKubeConfig(kubeContext)
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig: %w", err)
		}
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("building kubernetes client: %w", err)
		}
		return probeStaleNamespaces(ctx, client, namespaces)
	}
}

// probeStaleNamespaces is the testable core that accepts a kubernetes.Interface
// so callers can inject fake.NewSimpleClientset in unit tests.
//
// A namespace is considered stale when either:
//   - its DeletionTimestamp is set or its phase is Terminating (finalizer
//     deadlock — it will never complete without operator intervention), or
//   - it exists but holds no active Helm release (empty shell left by a partial
//     helm uninstall or a failed teardown that cleaned the release but not the
//     namespace).
//
// Helm 3 marks each release secret with the label owner=helm; absence of any
// such secret means no live Helm release occupies the namespace.
func probeStaleNamespaces(ctx context.Context, client kubernetes.Interface, namespaces []string) ([]StaleNamespace, error) {
	var stale []StaleNamespace
	for _, name := range namespaces {
		ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue // absent = healthy; the check only fires on unexpected presence
			}
			return stale, fmt.Errorf("get namespace %s: %w", name, err)
		}

		if ns.DeletionTimestamp != nil || ns.Status.Phase == corev1.NamespaceTerminating {
			stale = append(stale, StaleNamespace{Name: name, Reason: "stuck Terminating"})
			continue
		}

		// Limit to 1: only existence matters, not the full release history.
		// Check Secrets first (default Helm storage driver). If none exist,
		// also check ConfigMaps to handle HELM_DRIVER=configmap clusters —
		// both storage backends label their release objects with owner=helm.
		secrets, err := client.CoreV1().Secrets(name).List(ctx, metav1.ListOptions{
			LabelSelector: "owner=helm",
			Limit:         1,
		})
		if err != nil {
			return stale, fmt.Errorf("list Helm secrets in %s: %w", name, err)
		}
		if len(secrets.Items) > 0 {
			continue // healthy: active Helm release found via secret driver
		}
		cms, err := client.CoreV1().ConfigMaps(name).List(ctx, metav1.ListOptions{
			LabelSelector: "owner=helm",
			Limit:         1,
		})
		if err != nil {
			return stale, fmt.Errorf("list Helm configmaps in %s: %w", name, err)
		}
		if len(cms.Items) == 0 {
			stale = append(stale, StaleNamespace{Name: name, Reason: "no Helm release"})
		}
	}
	return stale, nil
}
