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
	"context"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// checkConfigurableNetworkPolicies inspects NetworkPolicy objects for each
// configured pair, verifying bidirectional coverage (A→B and B→A).
func checkConfigurableNetworkPolicies(
	ctx context.Context, client kubernetes.Interface,
	state *ValidationState, cfg *NetworkPoliciesConfig,
) {
	log := state.Log
	printHeader(log, "Configurable Network Policy Validation")

	allOK := true
	allCriticalOK := true
	hasCritical := false

	for i, pair := range cfg.Pairs {
		proto := corev1.Protocol(strings.ToUpper(pair.Protocol))
		port := pair.Port

		if i > 0 {
			log.Info("")
		}
		criticalLabel := ""
		if pair.Critical {
			criticalLabel = " [CRITICAL]"
			hasCritical = true
		}
		log.Infof("Pair: %s%s", pair.Name, criticalLabel)

		pairOK := true

		printFail := printWarning
		if pair.Critical {
			printFail = printError
		}

		hasIPBlock := false

		aToB := checkDirection(ctx, client,
			pair.A.Namespace, pair.A.PodSelector,
			pair.B.Namespace, pair.B.PodSelector,
			port, proto)
		if aToB.Allowed {
			printSuccess(log, fmt.Sprintf(
				"  A → B (%s → %s:%d/%s): Policies allow traffic",
				pair.A.Namespace, pair.B.Namespace, port, proto))
		} else {
			printFail(log, fmt.Sprintf(
				"  A → B (%s → %s:%d/%s): %s",
				pair.A.Namespace, pair.B.Namespace, port, proto, aToB.Reason))
			pairOK = false
			if aToB.HasIPBlock {
				hasIPBlock = true
			}
		}

		bToA := checkDirection(ctx, client,
			pair.B.Namespace, pair.B.PodSelector,
			pair.A.Namespace, pair.A.PodSelector,
			port, proto)
		if bToA.Allowed {
			printSuccess(log, fmt.Sprintf(
				"  B → A (%s → %s:%d/%s): Policies allow traffic",
				pair.B.Namespace, pair.A.Namespace, port, proto))
		} else {
			printFail(log, fmt.Sprintf(
				"  B → A (%s → %s:%d/%s): %s",
				pair.B.Namespace, pair.A.Namespace, port, proto, bToA.Reason))
			pairOK = false
			if bToA.HasIPBlock {
				hasIPBlock = true
			}
		}

		if !pairOK && hasIPBlock {
			printInfo(log, "  Note: IPBlock (CIDR) rules exist but cannot be evaluated statically — verify manually")
		}

		if !pairOK {
			allOK = false
			if pair.Critical {
				allCriticalOK = false
			}
		}
	}

	result := allOK
	state.ConfigurableNetPolOK = &result
	if hasCritical {
		state.ConfigurableNetPolCriticalOK = &allCriticalOK
	}

	log.Info("")
	if allOK {
		printSuccess(log, "All configurable network policy checks passed")
	} else {
		if !allCriticalOK {
			printError(log, "Critical network policy checks failed — cluster readiness affected")
			state.Recommendations = append(state.Recommendations,
				"Review NetworkPolicy objects for the failing critical namespace pairs "+
					"and ensure egress/ingress rules allow the required traffic.")
		}
		printWarning(log, "Some configurable network policy checks failed")
		state.Warnings = append(state.Warnings,
			"Configurable Network Policies: Some bidirectional policy checks did not pass")
	}
}

// directionResult holds the outcome of a single direction check.
type directionResult struct {
	Allowed    bool
	HasIPBlock bool
	// Reason provides additional context when Allowed is false.
	// Empty when Allowed is true.
	Reason string
}

// checkDirection evaluates whether Kubernetes NetworkPolicies permit traffic
// from srcNS to dstNS on the given port/protocol. It verifies:
//  1. Both namespaces exist
//  2. Egress from srcNS is not blocked (either not isolated or a rule allows dstNS)
//  3. Ingress to dstNS allows traffic from srcNS on the port
//
// It also reports whether any IPBlock (CIDR) rules were encountered but could
// not be evaluated, which may cause false positives.
func checkDirection(ctx context.Context, client kubernetes.Interface,
	srcNS string, srcSelector map[string]string,
	dstNS string, dstSelector map[string]string,
	port int, proto corev1.Protocol,
) directionResult {
	srcNSObj, err := client.CoreV1().Namespaces().Get(ctx, srcNS, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return directionResult{Reason: fmt.Sprintf("namespace %q does not exist", srcNS)}
		}
		return directionResult{Reason: fmt.Sprintf("failed to get namespace %q: %v", srcNS, err)}
	}
	dstNSObj, err := client.CoreV1().Namespaces().Get(ctx, dstNS, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return directionResult{Reason: fmt.Sprintf("namespace %q does not exist", dstNS)}
		}
		return directionResult{Reason: fmt.Sprintf("failed to get namespace %q: %v", dstNS, err)}
	}

	srcPodIPs := getPodIPs(ctx, client, srcNS)
	dstPodIPs := getPodIPs(ctx, client, dstNS)

	egressOK := egressAllowsTraffic(ctx, client,
		srcNS, srcSelector, dstNSObj.Labels, dstPodIPs, port, proto)
	ingressOK := ingressAllowsTraffic(ctx, client,
		dstNS, dstSelector, srcNSObj.Labels, srcPodIPs, port, proto)

	if egressOK && ingressOK {
		return directionResult{Allowed: true}
	}

	// Only flag HasIPBlock when IPBlock rules exist AND the target namespace
	// has no running pods (so we couldn't evaluate the CIDR). If pods exist,
	// the IPBlock was already evaluated against real IPs — no ambiguity.
	// Order matters: the cheap len() check short-circuits the additional
	// NetworkPolicy List call when pods are present.
	ipBlock := (len(dstPodIPs) == 0 && hasIPBlockPeers(ctx, client, srcNS, networkingv1.PolicyTypeEgress)) ||
		(len(srcPodIPs) == 0 && hasIPBlockPeers(ctx, client, dstNS, networkingv1.PolicyTypeIngress))

	var reason string
	if !egressOK && !ingressOK {
		reason = "blocked by both egress and ingress policies"
	} else if !egressOK {
		reason = "blocked by egress policy in " + srcNS
	} else {
		reason = "blocked by ingress policy in " + dstNS
	}

	return directionResult{
		Allowed:    false,
		HasIPBlock: ipBlock,
		Reason:     reason,
	}
}

// egressAllowsTraffic returns true when pods in srcNS (matching srcSelector)
// are permitted to send traffic to the destination namespace on port/proto.
//
// Kubernetes semantics: if no egress policy selects the pod, all egress is
// allowed. Once any egress policy selects the pod, only explicitly allowed
// destinations are reachable.
//
// dstPodIPs are the actual pod IPs in the destination namespace, used to
// evaluate IPBlock (CIDR) rules that cannot be resolved by namespace labels.
func egressAllowsTraffic(
	ctx context.Context, client kubernetes.Interface,
	srcNS string, srcSelector map[string]string,
	dstNSLabels map[string]string, dstPodIPs []string,
	port int, proto corev1.Protocol,
) bool {
	return policiesAllowTraffic(ctx, client, srcNS, srcSelector,
		networkingv1.PolicyTypeEgress, func(np *networkingv1.NetworkPolicy) bool {
			for _, rule := range np.Spec.Egress {
				if peersMatchNamespace(rule.To, srcNS, dstNSLabels, dstPodIPs) &&
					portsMatch(rule.Ports, port, proto) {
					return true
				}
			}
			return false
		})
}

// ingressAllowsTraffic returns true when pods in dstNS (matching dstSelector)
// are permitted to receive traffic from the source namespace on port/proto.
//
// Kubernetes semantics: if no ingress policy selects the pod, all ingress is
// allowed. Once any ingress policy selects the pod, only explicitly allowed
// sources are accepted.
//
// srcPodIPs are the actual pod IPs in the source namespace, used to
// evaluate IPBlock (CIDR) rules.
func ingressAllowsTraffic(
	ctx context.Context, client kubernetes.Interface,
	dstNS string, dstSelector map[string]string,
	srcNSLabels map[string]string, srcPodIPs []string,
	port int, proto corev1.Protocol,
) bool {
	return policiesAllowTraffic(ctx, client, dstNS, dstSelector,
		networkingv1.PolicyTypeIngress, func(np *networkingv1.NetworkPolicy) bool {
			for _, rule := range np.Spec.Ingress {
				if peersMatchNamespace(rule.From, dstNS, srcNSLabels, srcPodIPs) &&
					portsMatch(rule.Ports, port, proto) {
					return true
				}
			}
			return false
		})
}

// policiesAllowTraffic is the shared logic for both egress and ingress
// checks. It lists NetworkPolicies in the namespace, filters to those that
// isolate the pod for the given policyType, and returns true if either:
//   - no isolating policies exist (pod is not isolated → all traffic allowed), or
//   - at least one isolating policy has a rule matched by ruleAllows.
func policiesAllowTraffic(
	ctx context.Context, client kubernetes.Interface,
	ns string, podSelector map[string]string,
	policyType networkingv1.PolicyType,
	ruleAllows func(*networkingv1.NetworkPolicy) bool,
) bool {
	policies, err := client.NetworkingV1().NetworkPolicies(ns).List(
		ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}

	var isolating []*networkingv1.NetworkPolicy
	for i := range policies.Items {
		np := &policies.Items[i]
		if !matchesPodSelector(np.Spec.PodSelector, podSelector) {
			continue
		}
		if !hasPolicyType(np, policyType) {
			continue
		}
		isolating = append(isolating, np)
	}

	if len(isolating) == 0 {
		return true
	}

	for _, np := range isolating {
		if ruleAllows(np) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Peer matching
// ---------------------------------------------------------------------------

// peersMatchNamespace checks whether at least one peer in the list matches
// the target namespace. An empty/nil list matches everything (K8s semantics:
// empty "to"/"from" means all destinations/sources).
// targetPodIPs are the actual pod IPs in the target namespace for IPBlock evaluation.
func peersMatchNamespace(
	peers []networkingv1.NetworkPolicyPeer,
	policyNS string, targetNSLabels map[string]string,
	targetPodIPs []string,
) bool {
	if len(peers) == 0 {
		return true
	}
	for i := range peers {
		if peerMatchesNamespace(&peers[i], policyNS, targetNSLabels, targetPodIPs) {
			return true
		}
	}
	return false
}

// peerMatchesNamespace returns true when a single NetworkPolicyPeer would
// permit traffic to/from the namespace identified by targetNSLabels.
//
//   - IPBlock peers are evaluated against targetPodIPs when available.
//     If no pods exist in the target namespace, IPBlock cannot be evaluated
//     and returns false.
//   - A peer with only a PodSelector (no NamespaceSelector) applies to the
//     policy's own namespace only.
//   - A peer with a NamespaceSelector is matched against targetNSLabels.
func peerMatchesNamespace(
	peer *networkingv1.NetworkPolicyPeer,
	policyNS string, targetNSLabels map[string]string,
	targetPodIPs []string,
) bool {
	if peer.IPBlock != nil {
		return ipBlockMatchesAnyPod(peer.IPBlock, targetPodIPs)
	}

	if peer.NamespaceSelector == nil {
		return targetNSLabels[k8sNameLabel] == policyNS
	}

	return labelSelectorMatches(peer.NamespaceSelector, targetNSLabels)
}

// ipBlockMatchesAnyPod checks whether at least one pod IP is allowed by the
// IPBlock rule (within CIDR and not in any except range). Returns false when
// no pod IPs are available (namespace is empty).
func ipBlockMatchesAnyPod(ipBlock *networkingv1.IPBlock, podIPs []string) bool {
	if len(podIPs) == 0 {
		return false
	}

	_, cidr, err := net.ParseCIDR(ipBlock.CIDR)
	if err != nil {
		return false
	}

	excepts := make([]*net.IPNet, 0, len(ipBlock.Except))
	for _, e := range ipBlock.Except {
		_, exceptNet, err := net.ParseCIDR(e)
		if err != nil {
			continue
		}
		excepts = append(excepts, exceptNet)
	}

	for _, ipStr := range podIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if !cidr.Contains(ip) {
			continue
		}
		excluded := false
		for _, exceptNet := range excepts {
			if exceptNet.Contains(ip) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

// getPodIPs returns the IPs of all running pods in the given namespace.
// Reads Status.PodIPs to capture all addresses (IPv4 and IPv6 in dual-stack
// clusters) rather than just the primary Status.PodIP.
func getPodIPs(ctx context.Context, client kubernetes.Interface, ns string) []string {
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	var ips []string
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, podIP := range pod.Status.PodIPs {
			if podIP.IP != "" {
				ips = append(ips, podIP.IP)
			}
		}
	}
	return ips
}

const k8sNameLabel = "kubernetes.io/metadata.name"

// labelSelectorMatches evaluates a LabelSelector against a set of labels.
// A nil selector never matches. An empty selector (no matchLabels, no
// matchExpressions) matches everything.
func labelSelectorMatches(sel *metav1.LabelSelector, labels map[string]string) bool {
	if sel == nil {
		return false
	}
	for k, v := range sel.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, expr := range sel.MatchExpressions {
		if !matchExpression(expr, labels) {
			return false
		}
	}
	return true
}

func matchExpression(expr metav1.LabelSelectorRequirement, labels map[string]string) bool {
	val, exists := labels[expr.Key]
	switch expr.Operator {
	case metav1.LabelSelectorOpIn:
		if !exists {
			return false
		}
		for _, v := range expr.Values {
			if v == val {
				return true
			}
		}
		return false
	case metav1.LabelSelectorOpNotIn:
		if !exists {
			return true
		}
		for _, v := range expr.Values {
			if v == val {
				return false
			}
		}
		return true
	case metav1.LabelSelectorOpExists:
		return exists
	case metav1.LabelSelectorOpDoesNotExist:
		return !exists
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Pod selector matching
// ---------------------------------------------------------------------------

// matchesPodSelector returns true when the policy's podSelector is compatible
// with the requested labels. If the requested selector is empty, every policy
// matches. Otherwise both matchLabels and matchExpressions are evaluated.
func matchesPodSelector(
	policySelector metav1.LabelSelector, requested map[string]string,
) bool {
	if len(requested) == 0 {
		return true
	}
	return labelSelectorMatches(&policySelector, requested)
}

func hasPolicyType(np *networkingv1.NetworkPolicy, pt networkingv1.PolicyType) bool {
	for _, t := range np.Spec.PolicyTypes {
		if t == pt {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Port matching (shared by ingress and egress)
// ---------------------------------------------------------------------------

// hasIPBlockPeers checks whether any NetworkPolicy in the namespace contains
// IPBlock (CIDR) rules for the given policy type. This is used to detect
// potential false positives — the static analysis skips IPBlock peers because
// pod CIDRs are not known at analysis time.
func hasIPBlockPeers(ctx context.Context, client kubernetes.Interface,
	ns string, policyType networkingv1.PolicyType,
) bool {
	policies, err := client.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	for i := range policies.Items {
		np := &policies.Items[i]
		if !hasPolicyType(np, policyType) {
			continue
		}
		if policyType == networkingv1.PolicyTypeEgress {
			for _, rule := range np.Spec.Egress {
				for j := range rule.To {
					if rule.To[j].IPBlock != nil {
						return true
					}
				}
			}
		} else {
			for _, rule := range np.Spec.Ingress {
				for j := range rule.From {
					if rule.From[j].IPBlock != nil {
						return true
					}
				}
			}
		}
	}
	return false
}

// portsMatch checks whether the given port/protocol is allowed by the list
// of NetworkPolicyPort entries. An empty/nil list allows all ports.
func portsMatch(ports []networkingv1.NetworkPolicyPort, port int, proto corev1.Protocol) bool {
	if len(ports) == 0 {
		return true
	}
	for _, p := range ports {
		ruleProto := corev1.ProtocolTCP
		if p.Protocol != nil {
			ruleProto = *p.Protocol
		}
		if ruleProto != proto {
			continue
		}
		if p.Port == nil {
			return true
		}
		if p.Port.Type == intstr.Int && p.Port.IntValue() == port {
			return true
		}
		if p.EndPort != nil && p.Port.Type == intstr.Int {
			if port >= p.Port.IntValue() && port <= int(*p.EndPort) {
				return true
			}
		}
	}
	return false
}
