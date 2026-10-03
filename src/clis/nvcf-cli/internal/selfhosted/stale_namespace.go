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
	"os"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// nvcfControlPlaneNamespaces lists namespaces that a helmfile release deploys
// into on the control-plane cluster, per deploy/stacks/self-managed/helmfile.d,
// with the condition: that gates their releases and its base.yaml default.
// probeStaleNamespaces says which of them are left over from a failed or
// partial teardown.
//
// Only namespaces that actually host a release belong here: a namespace
// populated by something other than Helm (nvcf-backend, created at runtime by
// NVCA for worker pods) would be reported on a healthy cluster.
//
// The gateway controller namespace is deliberately absent: the release templates
// it from .Values.ingress.gatewayApi.controllerNamespace, so hardcoding
// envoy-gateway-system would probe a namespace the stack may not own.
// TestStaticNamespaces_MatchTheStacks pins this list to the stack.
var nvcfControlPlaneNamespaces = []stackNamespace{
	{name: "api-keys"},
	{name: "cassandra-system", gates: []string{"cassandra.enabled"}, on: true},
	{name: "cert-manager", gates: []string{"certManager.enabled"}, on: true},
	{name: "ess"},
	{name: "nats-system"},
	{name: "nvcf"},
	{name: "nvcf-ui", gates: []string{"addons.nvcfUi.enabled"}},
	{name: "sis", gates: []string{"icms.enabled"}, on: true},
	{name: "vault-system", gates: []string{"openbao.enabled"}, on: true},
}

// nvcfComputePlaneNamespaces lists namespaces that a helmfile release deploys
// into on the compute-plane cluster, per
// deploy/stacks/nvcf-compute-plane/helmfile.d. nvca-system is deliberately
// absent: it is operator-created and hosts no release. The scheduling addons
// are off by default, so their namespaces are probed only when the stack's
// values turn them on: a platform team's own KAI install is not NVCF's.
var nvcfComputePlaneNamespaces = []stackNamespace{
	{name: "dynamo-system", gates: []string{"addons.dynamoOperator.enabled"}},
	{name: "grove-system", gates: []string{"addons.groveOperator.enabled"}},
	{name: "kai-scheduler", gates: []string{"addons.kaiScheduler.enabled"}},
	{name: "nvca-operator"},
}

// Reasons a namespace is reported. Only StaleStuckTerminating fails the run.
const (
	// StaleStuckTerminating is a namespace Terminating past
	// namespaceStuckAfter, or one the namespace controller reports it cannot
	// finish deleting.
	StaleStuckTerminating = "stuck Terminating"
	// StaleTerminating is a namespace still within a normal deletion.
	StaleTerminating = "Terminating"
	// StaleReleaseMidOperation is a Helm release left pending or uninstalling.
	StaleReleaseMidOperation = "Helm release mid-operation"
	// StaleNoHelmRelease is a namespace holding what a removed install leaves.
	StaleNoHelmRelease = "no Helm release"
)

// namespaceStuckAfter is how long a namespace may stay Terminating before it
// is called stuck. Volume detach and PVC protection routinely hold a deleted
// namespace for tens of seconds, and `up` waits this long for one to go.
const namespaceStuckAfter = 2 * time.Minute

// StaleNamespace describes a single NVCF stack namespace that appears to be a
// leftover from a failed or partial teardown.
type StaleNamespace struct {
	Name   string
	Reason string // one of the Stale* reasons
	// Detail qualifies Reason: how long the namespace has been deleting and
	// why it is held, or the releases left mid-operation and their status.
	Detail string
	// Releases names the releases behind StaleReleaseMidOperation.
	Releases []string
}

// StaleNamespaceProber inspects the given namespaces and returns those that
// appear stale. The probe is read-only; it never deletes or modifies anything.
// A non-nil error lists the namespaces that could not be read; the returned
// slice still holds everything found in the others.
type StaleNamespaceProber func(ctx context.Context, kubeContext string, namespaces []string) ([]StaleNamespace, error)

// NewStaleNamespaceProber returns a StaleNamespaceProber backed by client-go.
// Chart-independent: works before any Helm release exists, and uses the
// operator's kubeconfig context to talk to the target cluster.
func NewStaleNamespaceProber() StaleNamespaceProber {
	return func(ctx context.Context, kubeContext string, namespaces []string) ([]StaleNamespace, error) {
		// The first contact with each visited cluster, so a cluster that
		// cannot be reached at all is reported here.
		client, err := connectCluster(ctx, kubeContext)
		if err != nil {
			return nil, err
		}
		return probeStaleNamespaces(ctx, client, namespaces)
	}
}

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

	// liveReleaseSelector matches the release records `helm upgrade --install`
	// builds on. An uninstalled record (kept with --keep-history) is not one.
	liveReleaseSelector = "owner=helm,status in (deployed,failed,superseded)"

	// midOperationReleaseSelector matches a release an interrupted install,
	// upgrade, rollback or teardown left behind. The next install fails on
	// it: "another operation is in progress", or "has no deployed releases".
	midOperationReleaseSelector = "owner=helm,status in (pending-install,pending-upgrade,pending-rollback,uninstalling)"
)

// helmReleaseLister lists one page of release objects and returns the labels
// of each.
type helmReleaseLister func(metav1.ListOptions) (labels []map[string]string, cont string, err error)

// helmRelease is one release object's name and status labels.
type helmRelease struct{ name, status string }

// findHelmReleases returns the release objects matching selector, paging
// until the server reports no more results, or until the first match when
// firstOnly is set.
//
// A single page with Limit set is not a valid existence test: the apiserver
// applies the label selector after paging, so a page can legitimately return
// zero items alongside a Continue token. A namespace like nvcf holds dozens of
// ServiceAccount tokens and TLS secrets that sort before sh.helm.release.v1.*,
// so the first page is routinely empty on a perfectly healthy install.
func findHelmReleases(list helmReleaseLister, selector string, firstOnly bool) ([]helmRelease, error) {
	opts := metav1.ListOptions{LabelSelector: selector, Limit: helmReleaseListPageSize}
	var found []helmRelease
	for page := 0; page < helmReleaseListMaxPages; page++ {
		items, cont, err := list(opts)
		if err != nil {
			return nil, err
		}
		for _, l := range items {
			found = append(found, helmRelease{name: l["name"], status: l["status"]})
		}
		if cont == "" || (firstOnly && len(found) > 0) {
			return found, nil
		}
		opts.Continue = cont
	}
	return nil, fmt.Errorf("gave up after %d pages scanning for Helm releases", helmReleaseListMaxPages)
}

// helmReleaseListers returns one lister per in-cluster Helm storage driver:
// Secrets (the default) and ConfigMaps (HELM_DRIVER=configmap). Both label
// release objects owner=helm, name and status.
func helmReleaseListers(ctx context.Context, client kubernetes.Interface, ns string) map[string]helmReleaseLister {
	return map[string]helmReleaseLister{
		"secrets": func(opts metav1.ListOptions) ([]map[string]string, string, error) {
			l, err := client.CoreV1().Secrets(ns).List(ctx, opts)
			if err != nil {
				return nil, "", err
			}
			out := make([]map[string]string, 0, len(l.Items))
			for i := range l.Items {
				out = append(out, l.Items[i].Labels)
			}
			return out, l.Continue, nil
		},
		"configmaps": func(opts metav1.ListOptions) ([]map[string]string, string, error) {
			l, err := client.CoreV1().ConfigMaps(ns).List(ctx, opts)
			if err != nil {
				return nil, "", err
			}
			out := make([]map[string]string, 0, len(l.Items))
			for i := range l.Items {
				out = append(out, l.Items[i].Labels)
			}
			return out, l.Continue, nil
		},
	}
}

// hasLiveWorkload reports whether namespace ns runs a workload: a Pending or
// Running pod that is not terminating and not a Job's. An install rendered
// with helm template, as Argo CD does, records no Helm release but runs these.
// Job pods are left out because Helm never deletes its hook Jobs, so a
// removed install leaves their Completed pods behind.
func hasLiveWorkload(ctx context.Context, client kubernetes.Interface, ns string) (bool, error) {
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("list pods: %w", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || ownedByJob(p) {
			continue
		}
		if p.Status.Phase == corev1.PodPending || p.Status.Phase == corev1.PodRunning {
			return true, nil
		}
	}
	return false, nil
}

func ownedByJob(p *corev1.Pod) bool {
	for _, ref := range p.OwnerReferences {
		if ref.Kind == "Job" {
			return true
		}
	}
	return false
}

// holdsInstallLeftovers reports whether namespace ns holds what a removed
// install leaves behind: volume claims, which helm never deletes and a
// reinstall would silently reattach, or workload objects such as the hook
// Jobs Helm also leaves. A namespace holding only what is created ahead of an
// install (pull and TLS Secrets, CA ConfigMaps, labels), or nothing at all,
// is a fresh one. One kind found is enough; an error means none was seen and
// at least one kind could not be read.
func holdsInstallLeftovers(ctx context.Context, client kubernetes.Interface, ns string) (bool, error) {
	one := metav1.ListOptions{Limit: 1}
	kinds := []struct {
		name  string
		count func() (int, error)
	}{
		{"persistentvolumeclaims", func() (int, error) {
			l, err := client.CoreV1().PersistentVolumeClaims(ns).List(ctx, one)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		}},
		{"jobs", func() (int, error) {
			l, err := client.BatchV1().Jobs(ns).List(ctx, one)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		}},
		{"statefulsets", func() (int, error) {
			l, err := client.AppsV1().StatefulSets(ns).List(ctx, one)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		}},
		{"deployments", func() (int, error) {
			l, err := client.AppsV1().Deployments(ns).List(ctx, one)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		}},
		{"daemonsets", func() (int, error) {
			l, err := client.AppsV1().DaemonSets(ns).List(ctx, one)
			if err != nil {
				return 0, err
			}
			return len(l.Items), nil
		}},
	}
	var errs []error
	for _, kind := range kinds {
		n, err := kind.count()
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s: %w", kind.name, err))
			continue
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, joinErrors(errs)
}

// joinedErrors keeps every error for errors.Is and errors.As, and renders
// them on one line, as a check row's message is.
type joinedErrors []error

func (e joinedErrors) Error() string {
	msgs := make([]string, 0, len(e))
	for _, err := range e {
		msgs = append(msgs, err.Error())
	}
	return strings.Join(msgs, "; ")
}

func (e joinedErrors) Unwrap() []error { return e }

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return joinedErrors(errs)
}

// helmReleasesAreInCluster reports whether Helm stores release state as
// in-cluster Secrets or ConfigMaps, the drivers this probe can see.
func helmReleasesAreInCluster() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("HELM_DRIVER")), "sql")
}

// namespaceDeletionFailures are the conditions the namespace controller sets
// when it cannot finish deleting a namespace's content on its own.
var namespaceDeletionFailures = []corev1.NamespaceConditionType{
	corev1.NamespaceDeletionDiscoveryFailure,
	corev1.NamespaceDeletionContentFailure,
	corev1.NamespaceDeletionGVParsingFailure,
}

// terminatingNamespace grades a namespace that is being deleted. It is stuck
// once it has been Terminating past namespaceStuckAfter, or as soon as the
// namespace controller reports a deletion failure; before that it is a normal
// deletion still draining volumes and finalizers.
func terminatingNamespace(ns *corev1.Namespace, now time.Time) StaleNamespace {
	out := StaleNamespace{Name: ns.Name, Reason: StaleTerminating}
	var details []string
	if ns.DeletionTimestamp != nil {
		age := now.Sub(ns.DeletionTimestamp.Time).Truncate(time.Second)
		details = append(details, "deleting for "+age.String())
		if age >= namespaceStuckAfter {
			out.Reason = StaleStuckTerminating
		}
	}
	for _, c := range ns.Status.Conditions {
		if c.Status == corev1.ConditionTrue && slices.Contains(namespaceDeletionFailures, c.Type) {
			out.Reason = StaleStuckTerminating
			details = append(details, string(c.Type)+": "+c.Message)
		}
	}
	out.Detail = strings.Join(details, "; ")
	return out
}

// probeStaleNamespaces is the testable core that accepts a kubernetes.Interface
// so callers can inject fake.NewSimpleClientset in unit tests.
//
// A namespace is reported when:
//   - it is being deleted (DeletionTimestamp set or phase Terminating), graded
//     by terminatingNamespace;
//   - a Helm release in it was left pending or uninstalling; or
//   - it holds no live Helm release, runs no workload, and still holds what a
//     removed install leaves (volume claims, or Jobs, StatefulSets,
//     Deployments or DaemonSets): a partial helm uninstall or a teardown that
//     removed the release but not its data.
//
// One namespace that cannot be read does not stop the others from being
// probed: every error is returned, joined and naming its namespace, next to
// everything that was found. Once ctx is done the probe stops, and the
// returned error carries ctx's error.
func probeStaleNamespaces(ctx context.Context, client kubernetes.Interface, namespaces []string) ([]StaleNamespace, error) {
	var stale []StaleNamespace
	var errs []error
	// HELM_DRIVER=sql keeps release state in a database, so no in-cluster
	// object says whether a release exists, and every namespace of a healthy
	// install would look stale. Inferring the driver from "no owner=helm
	// object anywhere" hid exactly the state `down` leaves behind: every
	// release destroyed, every namespace and PVC kept.
	inCluster := helmReleasesAreInCluster()
	for _, name := range namespaces {
		if ctx.Err() != nil {
			break
		}
		ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue // absent = healthy; the check only fires on unexpected presence
			}
			errs = append(errs, fmt.Errorf("get namespace %s: %w", name, err))
			continue
		}

		if ns.DeletionTimestamp != nil || ns.Status.Phase == corev1.NamespaceTerminating {
			stale = append(stale, terminatingNamespace(ns, time.Now()))
			continue
		}
		if !inCluster {
			continue
		}

		midOperation, live, err := probeHelmReleases(ctx, client, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if len(midOperation) > 0 {
			stale = append(stale, midOperationNamespace(name, midOperation))
			continue
		}
		if live {
			continue
		}
		// No release is not yet a leftover. The documented install pre-creates
		// namespaces, labelled and holding pull or TLS Secrets, so before the
		// first install none has a release; and an install rendered with
		// `helm template` (Argo CD) never records one but runs pods. What
		// `down` leaves behind runs nothing, and holds volume claims or the
		// hook Jobs Helm never deletes. A namespace that cannot be read is
		// listed as such rather than reported as a leftover nobody saw.
		live, err = hasLiveWorkload(ctx, client, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if live {
			continue
		}
		leftovers, err := holdsInstallLeftovers(ctx, client, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if leftovers {
			stale = append(stale, StaleNamespace{Name: name, Reason: StaleNoHelmRelease})
		}
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	return stale, joinErrors(errs)
}

// probeHelmReleases returns the releases in namespace ns left pending or
// uninstalling, and whether a live release occupies it.
func probeHelmReleases(ctx context.Context, client kubernetes.Interface, ns string) ([]helmRelease, bool, error) {
	listers := helmReleaseListers(ctx, client, ns)
	var midOperation []helmRelease
	for _, kind := range []string{"secrets", "configmaps"} {
		found, err := findHelmReleases(listers[kind], midOperationReleaseSelector, false)
		if err != nil {
			return nil, false, fmt.Errorf("list Helm %s: %w", kind, err)
		}
		midOperation = append(midOperation, found...)
	}
	if len(midOperation) > 0 {
		return midOperation, false, nil
	}
	for _, kind := range []string{"secrets", "configmaps"} {
		found, err := findHelmReleases(listers[kind], liveReleaseSelector, true)
		if err != nil {
			return nil, false, fmt.Errorf("list Helm %s: %w", kind, err)
		}
		if len(found) > 0 {
			return nil, true, nil
		}
	}
	return nil, false, nil
}

// midOperationNamespace reports namespace ns for the releases in it left
// pending or uninstalling, each named once with its status.
func midOperationNamespace(ns string, releases []helmRelease) StaleNamespace {
	out := StaleNamespace{Name: ns, Reason: StaleReleaseMidOperation}
	var details []string
	for _, r := range releases {
		if r.name == "" || slices.Contains(out.Releases, r.name) {
			continue
		}
		out.Releases = append(out.Releases, r.name)
		details = append(details, r.name+" "+r.status)
	}
	out.Detail = strings.Join(details, ", ")
	return out
}
