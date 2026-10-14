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

package clustervalidator

import (
	"maps"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// haTier is the HA convention highAvailability.mode preferred or enforced
// renders for a workload.
type haTier int

const (
	// haReplicaSafe Deployments run 2 or more replicas behind a PDB with
	// minAvailable 1, roll out surge-first, and spread over nodes and zones.
	haReplicaSafe haTier = iota
	// haSingleton Deployments run exactly 1 replica: callbacks to them are
	// bound to the pod that took the request.
	haSingleton
	// haQuorum StatefulSets run 3 or more members behind a PDB that allows one
	// voluntary disruption, one member per node, spread over zones.
	haQuorum
)

// haWorkload is one release of the self-managed stack that the HA convention
// covers, keyed by its namespace and the release name its pods carry as
// haInstanceLabel.
type haWorkload struct {
	namespace string
	release   string
	// podLabels tell the workload's pods from the release's other pods, such
	// as nats-box beside the NATS servers and the agent injector beside the
	// OpenBao servers.
	podLabels map[string]string
	tier      haTier
	// optional releases are installed only when a feature or addon enables
	// them, so they are judged only when found.
	optional bool
	// component is the NVCF_EXTERNAL_COMPONENTS name of a quorum member that
	// may run outside the cluster.
	component string
}

// haWorkloads is the HA convention of the self-managed stack, release by
// release, as deploy/stacks/self-managed renders it. Update it when the stack
// adds, renames, moves or reclassifies a release.
var haWorkloads = []haWorkload{
	{namespace: "nvcf", release: "api", tier: haReplicaSafe},
	{namespace: "nvcf", release: "nvct-api", tier: haReplicaSafe},
	{namespace: "nvcf", release: "notary-service", tier: haReplicaSafe},
	{namespace: "nvcf", release: "reval", tier: haReplicaSafe},
	{namespace: "nvcf", release: "ratelimiter", tier: haReplicaSafe, optional: true},
	{namespace: "nvcf", release: "llm-api-gateway", tier: haReplicaSafe, optional: true},
	{namespace: "ess", release: "ess-api", tier: haReplicaSafe},
	{namespace: "api-keys", release: "api-keys", tier: haReplicaSafe},
	{namespace: "api-keys", release: "admin-issuer-proxy", tier: haReplicaSafe},
	{namespace: "sis", release: "sis", tier: haReplicaSafe, optional: true},
	{namespace: "nats-system", release: "nats-auth-callout-service", tier: haReplicaSafe},
	{namespace: "nvcf", release: "grpc-proxy", tier: haSingleton},
	{namespace: "nvcf", release: "invocation-service", tier: haSingleton},
	{namespace: "nats-system", release: "nats", tier: haQuorum, component: "nats",
		podLabels: map[string]string{"app.kubernetes.io/component": "nats"}},
	{namespace: "vault-system", release: "openbao-server", tier: haQuorum, component: "openbao",
		podLabels: map[string]string{"component": "server"}},
	{namespace: "cassandra-system", release: "cassandra", tier: haQuorum, component: "cassandra"},
}

// haQuorumMember reports whether sts is the StatefulSet of a quorum workload of
// haWorkloads, one whose placement the HA Guarantee row judges.
func haQuorumMember(sts *appsv1.StatefulSet) bool {
	return slices.ContainsFunc(haWorkloads, func(w haWorkload) bool {
		return w.tier == haQuorum && w.namespaceName() == sts.Namespace && w.owns(&sts.Spec.Template)
	})
}

// namespaceName is where w runs: a quorum member follows its component's
// namespace override.
func (w haWorkload) namespaceName() string {
	for _, c := range knownQuorumComponents {
		if c.name == w.component {
			return c.namespaceName()
		}
	}
	return w.namespace
}

// The workload kinds the stack renders for the HA convention.
const (
	haDeploymentKind  = "Deployment"
	haStatefulSetKind = "StatefulSet"
)

// kind is the workload kind the stack renders for w.
func (w haWorkload) kind() string {
	if w.tier == haQuorum {
		return haStatefulSetKind
	}
	return haDeploymentKind
}

// selector is the pod labels that make a pod template w's.
func (w haWorkload) selector() labels.Set {
	set := labels.Set{haInstanceLabel: w.release}
	maps.Copy(set, w.podLabels)
	return set
}

// owns reports whether tpl is the pod template of w.
func (w haWorkload) owns(tpl *corev1.PodTemplateSpec) bool {
	return labels.SelectorFromSet(w.selector()).Matches(labels.Set(tpl.Labels))
}

// haNamespaces lists the namespaces of the workloads of tiers, in table order.
func haNamespaces(tiers ...haTier) []string {
	var out []string
	for _, w := range haWorkloads {
		if ns := w.namespaceName(); slices.Contains(tiers, w.tier) && !slices.Contains(out, ns) {
			out = append(out, ns)
		}
	}
	return out
}
