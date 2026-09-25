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

// nvcfControlPlaneNamespaces lists namespaces that a helmfile release deploys
// into on the control-plane cluster, per deploy/stacks/self-managed/helmfile.d.
// Any of these that exist without an active Helm release, or that are stuck
// Terminating, are leftover from a failed or partial teardown.
//
// Only namespaces that actually host a release belong here: the remediation for
// a hit is "delete this namespace", so a namespace populated by something other
// than Helm (nvcf-backend, created at runtime by NVCA for worker pods) would be
// reported stale on a healthy cluster and deleting it would destroy live work.
//
// The gateway controller namespace is deliberately absent: the release templates
// it from .Values.ingress.gatewayApi.controllerNamespace, so hardcoding
// envoy-gateway-system would probe a namespace the stack may not own.
var nvcfControlPlaneNamespaces = []string{
	"api-keys", "cassandra-system", "cert-manager", "ess",
	"nats-system", "nvcf", "nvcf-ui", "sis", "vault-system",
}

// nvcfComputePlaneNamespaces lists namespaces that a helmfile release deploys
// into on the compute-plane cluster, per
// deploy/stacks/nvcf-compute-plane/helmfile.d. nvca-system is deliberately
// absent: it is operator-created and hosts no release.
var nvcfComputePlaneNamespaces = []string{
	"dynamo-system", "grove-system", "kai-scheduler", "nvca-operator",
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
//     deadlock; it will never complete without operator intervention), or
//   - it exists but holds no active Helm release (empty shell left by a partial
//     helm uninstall or a failed teardown that cleaned the release but not the
//     namespace).
//
// Helm 3 marks each release secret with the label owner=helm; absence of any
// such secret means no live Helm release occupies the namespace.
const (
	// helmReleaseListPageSize bounds each page of the owner=helm scan. A
	// namespace holds at most a handful of release objects, so this is only a
	// ceiling on how much is pulled per round trip while paging past
	// non-matching objects.
	helmReleaseListPageSize = 100

	// helmReleaseListMaxPages stops the scan even if the server keeps handing
	// back a Continue token. At the page size above this covers 100k objects in
	// one namespace, far past anything real, and guarantees termination.
	helmReleaseListMaxPages = 1000
)

// helmReleaseExists reports whether any owner=helm object exists, paging until
// it finds one or the server reports no more results.
//
// A single page with Limit set is not a valid existence test: the apiserver
// applies the label selector after paging, so a page can legitimately return
// zero items alongside a Continue token. A namespace like nvcf holds dozens of
// ServiceAccount tokens and TLS secrets that sort before sh.helm.release.v1.*,
// so the first page is routinely empty on a perfectly healthy install.
func helmReleaseExists(
	list func(metav1.ListOptions) (count int, cont string, err error),
) (bool, error) {
	opts := metav1.ListOptions{LabelSelector: "owner=helm", Limit: helmReleaseListPageSize}
	for page := 0; page < helmReleaseListMaxPages; page++ {
		count, cont, err := list(opts)
		if err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
		if cont == "" {
			return false, nil
		}
		opts.Continue = cont
	}
	return false, fmt.Errorf("gave up after %d pages scanning for Helm releases", helmReleaseListMaxPages)
}

func probeStaleNamespaces(ctx context.Context, client kubernetes.Interface, namespaces []string) ([]StaleNamespace, error) {
	var stale []StaleNamespace
	// noRelease is held back until we know the Helm storage driver keeps its
	// state in-cluster at all; see the gate below.
	var noRelease []string
	anyHelmReleaseSeen := false
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

		// Check Secrets first (the default Helm storage driver), then ConfigMaps
		// for HELM_DRIVER=configmap clusters. Both label release objects
		// owner=helm.
		found, err := helmReleaseExists(
			func(opts metav1.ListOptions) (int, string, error) {
				l, lerr := client.CoreV1().Secrets(name).List(ctx, opts)
				if lerr != nil {
					return 0, "", lerr
				}
				return len(l.Items), l.Continue, nil
			})
		if err != nil {
			return stale, fmt.Errorf("list Helm secrets in %s: %w", name, err)
		}
		if found {
			anyHelmReleaseSeen = true
			continue // healthy: active Helm release found via the secret driver
		}
		found, err = helmReleaseExists(
			func(opts metav1.ListOptions) (int, string, error) {
				l, lerr := client.CoreV1().ConfigMaps(name).List(ctx, opts)
				if lerr != nil {
					return 0, "", lerr
				}
				return len(l.Items), l.Continue, nil
			})
		if err != nil {
			return stale, fmt.Errorf("list Helm configmaps in %s: %w", name, err)
		}
		if found {
			anyHelmReleaseSeen = true
			continue
		}
		noRelease = append(noRelease, name)
	}

	// Only trust the "no Helm release" signal when at least one owner=helm
	// object was seen somewhere. HELM_DRIVER=sql keeps release state in a
	// database with no in-cluster object at all, so without this every
	// namespace of a healthy production install reports stale.
	if anyHelmReleaseSeen {
		for _, name := range noRelease {
			stale = append(stale, StaleNamespace{Name: name, Reason: "no Helm release"})
		}
	}
	return stale, nil
}
