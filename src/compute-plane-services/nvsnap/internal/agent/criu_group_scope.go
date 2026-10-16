// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// A CRIU checkpoint holds the process's memory: its tokens, keys and
// request data. The configuration hash cannot tell two tenants apart (it
// hashes secret references, not their values), so a group record is keyed
// by the configuration and the tenant scope together, and a pod is only
// ever restored from a capture of its own scope.
//
// The scope comes from the namespace, never the pod: a pod's labels are
// the tenant's to write, the platform's namespace labels are not.

// criuScopeLabels name the namespace labels that identify the tenant
// workload, most specific first. NVCF labels each function namespace
// with its function version.
var criuScopeLabels = []string{"function-version-id", "FUNCTION_VERSION_ID"}

const criuScopeTTL = 5 * time.Minute

type criuScopeEntry struct {
	scope string
	at    time.Time
}

var criuScopeCache sync.Map // namespace -> criuScopeEntry

// criuGroupScope returns the tenant scope of namespace ns: its function
// version when the platform labelled it, else the namespace itself.
func (a *Agent) criuGroupScope(ctx context.Context, ns string) (string, error) {
	if a.kubeClient == nil {
		return criuScopeOf(ns, nil), nil
	}
	if e, ok := criuScopeCache.Load(ns); ok && time.Since(e.(criuScopeEntry).at) < criuScopeTTL {
		return e.(criuScopeEntry).scope, nil
	}
	n, err := a.kubeClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	scope := criuScopeOf(ns, n.Labels)
	if strings.HasPrefix(scope, "function-version/") {
		// Only the label is cached: it may be put on after the namespace
		// is created, and a capture filed under the namespace would never
		// be found by its function's next deploy.
		criuScopeCache.Store(ns, criuScopeEntry{scope: scope, at: time.Now()})
	}
	return scope, nil
}

func criuScopeOf(ns string, labels map[string]string) string {
	for _, l := range criuScopeLabels {
		if v := labels[l]; v != "" {
			return "function-version/" + v
		}
	}
	return webhook.CRIUNamespaceScope(ns)
}

// criuGroupKey is the group record key of a configuration (its cache URI)
// in a tenant scope.
func criuGroupKey(cacheURI, scope string) string {
	sum := sha256.Sum256([]byte(strings.TrimPrefix(cacheURI, "cache://") + "\x00" + scope))
	return hex.EncodeToString(sum[:8])
}

// lookupCRIUGroupFor is the webhook's view of the group records: the
// complete capture of uri in namespace ns's scope.
func (a *Agent) lookupCRIUGroupFor(ctx context.Context, ns, uri string) (webhook.CRIUGroup, bool) {
	scope, err := a.criuGroupScope(ctx, ns)
	if err != nil {
		a.log.WithError(err).WithField("namespace", ns).Warn("CRIU group restore: cannot read the namespace's scope; cold start")
		return webhook.CRIUGroup{}, false
	}
	return a.lookupCRIUGroup(ctx, criuGroupKey(uri, scope))
}

// criuGroupAdmits reports whether pod, a rank of an instance of several
// pods, can be admitted as its rank's group restore placeholder. The restore runs once every rank of the instance
// is a placeholder, driven by rank 0, so it needs all of them created
// together and none already running:
//   - a rank replaced while the others run (a node lost, a pod deleted)
//     would wait for a restore no one starts;
//   - a StatefulSet that creates its pods in order waits for rank 0 to be
//     Ready before it creates rank 1, and rank 0's placeholder is not
//     Ready until the instance is restored.
//
// A pod that cannot restore as a group starts cold.
func (a *Agent) criuGroupAdmits(ctx context.Context, pod *corev1.Pod) (bool, string) {
	if c := metav1.GetControllerOf(pod); c != nil && c.Kind == "StatefulSet" && groveInstance(pod) == "" && pod.Labels[lwsGroupKeyLabel] == "" {
		sts, err := a.kubeClient.AppsV1().StatefulSets(pod.Namespace).Get(ctx, c.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("cannot read StatefulSet %s: %v", c.Name, err)
		}
		if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
			return false, fmt.Sprintf("StatefulSet %s creates its pods in order (%s); a group restore needs them together (Parallel)",
				c.Name, appsv1.OrderedReadyPodManagement)
		}
	}
	pods, err := a.kubeClient.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Sprintf("cannot list the instance's pods: %v", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Name == pod.Name || p.DeletionTimestamp != nil || !sameInstance(pod, p) {
			continue
		}
		if p.Annotations[webhook.CRIUGroupAnnotation] != "" && p.Annotations[webhook.CRIURestoredAnnotation] == "" {
			continue // another placeholder of this restore
		}
		return false, fmt.Sprintf("pod %s of the instance already runs; this pod replaces one rank", p.Name)
	}
	return true, ""
}
