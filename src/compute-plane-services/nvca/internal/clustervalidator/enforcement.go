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
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

const (
	enforcementTestPort    = 8080
	enforcementDefaultImg  = "busybox:1.36"
	enforcementConnTimeout = 5
	enforcementPodTimeout  = 90 * time.Second
	enforcementPropDelay   = 3 * time.Second
	enforcementServerPod   = "netpol-server"
	enforcementIngressPol  = "netpol-test-ingress"
	enforcementEgressPol   = "netpol-test-egress"
)

func enforcementResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10m"),
			corev1.ResourceMemory: resource.MustParse("16Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
}

// enforcementEnv holds the shared state for a single enforcement test run.
type enforcementEnv struct {
	ctx      context.Context
	client   kubernetes.Interface
	log      *logrus.Entry
	ns       string
	image    string
	serverIP string
	timeout  time.Duration
	probeSeq int
}

func (e *enforcementEnv) nextProbe(role string) string {
	e.probeSeq++
	return fmt.Sprintf("probe-%s-%d", role, e.probeSeq)
}

func (e *enforcementEnv) probe(role string) (bool, error) {
	return probeConnectivity(
		e.ctx, e.client, e.ns, e.nextProbe(role),
		e.image, role, e.serverIP, e.timeout,
	)
}

func (e *enforcementEnv) probeWithDelay(role string, delay int) (bool, error) {
	return probeConnectivityWithDelay(
		e.ctx, e.client, e.ns, e.nextProbe(role),
		e.image, role, e.serverIP, e.timeout, delay,
	)
}

// checkNetworkPolicyEnforcement deploys ephemeral workloads in a temporary
// namespace, applies NetworkPolicy objects, and verifies the CNI data-plane
// actually enforces them.
func checkNetworkPolicyEnforcement(
	ctx context.Context, client kubernetes.Interface,
	state *ValidationState, cfg *EnforcementConfig,
) {
	log := state.Log
	printHeader(log, "Network Policy Enforcement Validation")

	if cfg == nil || !cfg.Enabled {
		printInfo(log, "Enforcement testing not enabled — skipping")
		return
	}

	state.EnforcementCritical = cfg.Critical

	image := cfg.TestImage
	if image == "" {
		image = enforcementDefaultImg
	}

	podTimeout := enforcementPodTimeout
	if cfg.TimeoutSeconds > 0 {
		podTimeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}

	ns := fmt.Sprintf("netpol-validation-%d", time.Now().UnixNano()%100000)
	defer cleanupTestNamespace(log, client, ns)

	env, ok := setupEnforcementEnv(ctx, client, state, ns, image, podTimeout)
	if !ok {
		return
	}

	if !runBaselinePhase(env, state) {
		return
	}

	denyAllOK := runDenyAllIngressPhase(env, state)
	selectiveOK := runSelectiveAllowPhase(env, state)
	egressOK := runEgressPhase(env)

	allOK := denyAllOK && selectiveOK
	state.EnforcementOK = &allOK

	log.Info("")
	if allOK && egressOK {
		printSuccess(log, "Network Policy Enforcement: FULLY VERIFIED")
	} else if allOK {
		printSuccess(log, "Network Policy Enforcement: Ingress verified")
		if !egressOK {
			printWarning(log, "Network Policy Enforcement: Egress not fully verified (non-critical)")
			state.Warnings = append(state.Warnings,
				"Network Policy Enforcement: Egress enforcement not verified (non-critical)")
		}
	} else {
		printError(log, "Network Policy Enforcement: VALIDATION FAILED")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: CNI does not enforce NetworkPolicies")
		state.Recommendations = append(state.Recommendations,
			"Verify your CNI plugin supports and enforces NetworkPolicies "+
				"(Calico, Cilium). Flannel does NOT support NetworkPolicies.")
	}
}

func setupEnforcementEnv(
	ctx context.Context, client kubernetes.Interface,
	state *ValidationState, ns, image string, podTimeout time.Duration,
) (*enforcementEnv, bool) {
	log := state.Log
	log.Info("Phase 1: Setting up test environment")
	printInfo(log, fmt.Sprintf("Creating namespace %s", ns))

	if err := createTestNamespace(ctx, client, ns); err != nil {
		printError(log, fmt.Sprintf("Failed to create test namespace: %v", err))
		state.Warnings = append(state.Warnings, "Network Policy Enforcement: setup failed")
		return nil, false
	}
	printSuccess(log, "Namespace created")

	printInfo(log, "Deploying server pod...")
	if err := createServerPod(ctx, client, ns, image); err != nil {
		printError(log, fmt.Sprintf("Failed to create server pod: %v", err))
		state.Warnings = append(state.Warnings, "Network Policy Enforcement: setup failed")
		return nil, false
	}

	if err := waitForPodReady(ctx, client, ns, enforcementServerPod, podTimeout); err != nil {
		printError(log, fmt.Sprintf("Server pod not ready: %v", err))
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: server pod failed to start")
		return nil, false
	}

	serverIP, err := getPodIP(ctx, client, ns, enforcementServerPod)
	if err != nil {
		printError(log, fmt.Sprintf("Could not get server IP: %v", err))
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: server pod has no IP")
		return nil, false
	}
	printSuccess(log, fmt.Sprintf("Server pod ready at %s:%d", serverIP, enforcementTestPort))

	return &enforcementEnv{
		ctx: ctx, client: client, log: log, ns: ns,
		image: image, serverIP: serverIP, timeout: podTimeout,
	}, true
}

func runBaselinePhase(env *enforcementEnv, state *ValidationState) bool {
	log := env.log
	log.Info("")
	log.Info("Phase 2: Baseline connectivity (no policies)")
	printBlue(log, "Testing: client → server (expect: allowed)")

	clientOK, err := env.probe("client")
	if err != nil {
		printError(log, fmt.Sprintf("Baseline probe error: %v", err))
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: baseline probe failed")
		return false
	}
	if !clientOK {
		printError(log, "Baseline: client cannot reach server — aborting")
		printError(log, "Network connectivity is broken even without policies")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: baseline connectivity broken")
		return false
	}
	printSuccess(log, "Baseline: client → server works")

	printBlue(log, "Testing: allowed-client → server (expect: allowed)")
	allowedOK, err := env.probe("allowed")
	if err != nil || !allowedOK {
		printError(log, "Baseline: allowed-client cannot reach server — aborting")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: baseline connectivity broken")
		return false
	}
	printSuccess(log, "Baseline: allowed-client → server works")
	printSuccess(log, "Baseline connectivity verified — test harness is working")
	return true
}

func runDenyAllIngressPhase(env *enforcementEnv, state *ValidationState) bool {
	log := env.log
	log.Info("")
	log.Info("Phase 3: Deny-all ingress enforcement")
	printInfo(log, "Applying deny-all ingress policy on server pod...")
	if err := applyDenyAllIngressPolicy(env.ctx, env.client, env.ns); err != nil {
		printError(log, fmt.Sprintf("Failed to apply deny-all policy: %v", err))
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: could not apply policy")
		return false
	}
	printSuccess(log, "Deny-all ingress policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy to propagate to data plane...",
		enforcementPropDelay))
	time.Sleep(enforcementPropDelay)

	printBlue(log, "Testing: client → server (expect: blocked)")
	clientReached, _ := env.probe("client")
	printBlue(log, "Testing: allowed-client → server (expect: blocked)")
	allowedReached, _ := env.probe("allowed")

	ok := !clientReached && !allowedReached
	if ok {
		printSuccess(log, "Deny-all ingress enforcement VERIFIED")
	} else {
		if clientReached {
			printError(log, "FAIL: Client can STILL reach server after deny-all policy")
		}
		if allowedReached {
			printError(log, "FAIL: Allowed client can STILL reach server under deny-all")
		}
		printError(log, "NetworkPolicy enforcement is NOT working")
		printError(log, "Your CNI plugin accepts NetworkPolicy objects but does not enforce them")
	}
	return ok
}

func runSelectiveAllowPhase(env *enforcementEnv, state *ValidationState) bool {
	log := env.log
	log.Info("")
	log.Info("Phase 4: Selective allow rule validation")
	printInfo(log, "Applying selective allow policy (role=allowed only)...")
	if err := applySelectiveAllowPolicy(env.ctx, env.client, env.ns); err != nil {
		printError(log, fmt.Sprintf("Failed to apply selective allow policy: %v", err))
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: could not apply selective policy")
		return false
	}
	printSuccess(log, "Selective allow policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy update to propagate...",
		enforcementPropDelay))
	time.Sleep(enforcementPropDelay)

	printBlue(log, "Testing: client → server (expect: blocked)")
	clientReached, _ := env.probe("client")
	printBlue(log, "Testing: allowed-client → server (expect: allowed)")
	allowedReached, _ := env.probe("allowed")

	ok := !clientReached && allowedReached
	if ok {
		printSuccess(log, "Selective allow rule enforcement VERIFIED")
	} else {
		if clientReached {
			printError(log, "FAIL: Unlabeled client can reach server despite selective policy")
		}
		if !allowedReached {
			printError(log, "FAIL: Allowed client is blocked despite matching allow rule")
			printError(log, "The CNI may not correctly evaluate podSelector in ingress rules")
		}
	}
	return ok
}

// egressSettleDelay gives the CNI time to program eBPF/iptables rules on a
// newly-created probe pod's veth before wget runs.
const egressSettleDelay = 3 // seconds

func runEgressPhase(env *enforcementEnv) bool {
	log := env.log
	log.Info("")
	log.Info("Phase 5: Egress policy enforcement")

	printInfo(log, "Removing ingress policy for clean egress test...")
	if err := deleteNetworkPolicy(env.ctx, env.client, env.ns, enforcementIngressPol); err != nil {
		printWarning(log, fmt.Sprintf("Could not remove ingress policy: %v", err))
	}
	time.Sleep(enforcementPropDelay)

	printInfo(log, "Verifying connectivity restored (clean slate)...")
	cleanSlate, _ := env.probe("client")
	if !cleanSlate {
		printWarning(log, "Client still cannot reach server after policy removal — stale state")
		printWarning(log, "Skipping egress test")
		return false
	}
	printSuccess(log, "Connectivity restored after policy removal")

	printInfo(log, "Applying deny-all egress policy on client pod...")
	if err := applyDenyAllEgressPolicy(env.ctx, env.client, env.ns); err != nil {
		printError(log, fmt.Sprintf("Failed to apply egress policy: %v", err))
		return false
	}
	printSuccess(log, "Deny-all egress policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy to propagate...", enforcementPropDelay))
	time.Sleep(enforcementPropDelay)

	printBlue(log, "Testing: client → server (expect: blocked by egress)")
	clientReached, _ := env.probeWithDelay("client", egressSettleDelay)
	printBlue(log, "Testing: allowed-client → server (expect: allowed, unaffected)")
	allowedReached, _ := env.probeWithDelay("allowed", egressSettleDelay)

	ok := !clientReached && allowedReached
	if ok {
		printSuccess(log, "Egress policy enforcement VERIFIED")
	} else {
		if clientReached {
			printWarning(log, "Client can still reach server despite egress deny-all")
			printWarning(log, "Egress policy enforcement may not be supported by your CNI")
		}
		if !allowedReached {
			printWarning(log, "Allowed client also blocked — possible over-broad policy application")
		}
	}
	return ok
}

// ---------------------------------------------------------------------------
// Namespace helpers
// ---------------------------------------------------------------------------

func createTestNamespace(ctx context.Context, client kubernetes.Interface, ns string) error {
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{"app": "netpol-validation", "purpose": "enforcement-test"},
		},
	}, metav1.CreateOptions{})
	return err
}

func cleanupTestNamespace(log *logrus.Entry, client kubernetes.Interface, ns string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	printInfo(log, fmt.Sprintf("Cleaning up test namespace %s", ns))
	err := client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		log.Warnf("Failed to clean up namespace %s: %v", ns, err)
	}
}

// ---------------------------------------------------------------------------
// Pod helpers
// ---------------------------------------------------------------------------

func buildServerPod(ns, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      enforcementServerPod,
			Namespace: ns,
			Labels:    map[string]string{"app": "netpol-test", "role": "server"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "server",
				Image: image,
				Command: []string{
					"sh", "-c",
					fmt.Sprintf("mkdir -p /tmp/www && echo 'netpol-test-ok' > /tmp/www/index.html && httpd -f -p %d -h /tmp/www", enforcementTestPort),
				},
				Ports: []corev1.ContainerPort{{
					ContainerPort: int32(enforcementTestPort),
					Protocol:      corev1.ProtocolTCP,
				}},
				Resources: enforcementResources(),
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

func createServerPod(ctx context.Context, client kubernetes.Interface, ns, image string) error {
	_, err := client.CoreV1().Pods(ns).Create(ctx, buildServerPod(ns, image), metav1.CreateOptions{})
	return err
}

// buildProbePod constructs a short-lived pod that runs wget to test
// connectivity. settleDelay adds a sleep before wget, giving the CNI time
// to program egress rules on newly-created pods (needed because egress
// policies apply to the source pod, which may not have rules yet at start).
func buildProbePod(ns, name, image, role, serverIP string, settleDelay int) *corev1.Pod {
	wgetCmd := fmt.Sprintf("wget -q -O- --timeout=%d http://%s:%d/", enforcementConnTimeout, serverIP, enforcementTestPort)
	cmd := wgetCmd
	if settleDelay > 0 {
		cmd = fmt.Sprintf("sleep %d && %s", settleDelay, wgetCmd)
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app": "netpol-test", "role": role},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:      "probe",
				Image:     image,
				Command:   []string{"sh", "-c", cmd},
				Resources: enforcementResources(),
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// probeConnectivity creates a short-lived pod that attempts to reach the
// server via wget. Returns true if the pod succeeds (connectivity works),
// false if it fails (connectivity blocked or timed out).
func probeConnectivity(
	ctx context.Context, client kubernetes.Interface,
	ns, name, image, role, serverIP string, timeout time.Duration,
) (bool, error) {
	return probeConnectivityWithDelay(ctx, client, ns, name, image, role, serverIP, timeout, 0)
}

// probeConnectivityWithDelay is like probeConnectivity but adds an in-pod
// sleep before wget runs. This gives the CNI time to program egress rules
// on the newly-created pod.
func probeConnectivityWithDelay(
	ctx context.Context, client kubernetes.Interface,
	ns, name, image, role, serverIP string,
	timeout time.Duration, settleDelay int,
) (bool, error) {
	pod := buildProbePod(ns, name, image, role, serverIP, settleDelay)
	if _, err := client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return false, fmt.Errorf("creating probe pod %s: %w", name, err)
	}
	defer func() {
		_ = client.CoreV1().Pods(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	}()

	return waitForPodDone(ctx, client, ns, name, timeout)
}

// waitForPodReady polls until the named pod has the Ready condition.
func waitForPodReady(ctx context.Context, client kubernetes.Interface, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("pod %s/%s did not become ready within %v", ns, name, timeout)
		}

		pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("getting pod %s/%s: %w", ns, name, err)
			}
		} else {
			if isPodReady(pod) {
				return nil
			}
			if pod.Status.Phase == corev1.PodFailed {
				return fmt.Errorf("pod %s/%s entered Failed phase", ns, name)
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitForPodDone polls until the named pod reaches Succeeded or Failed.
// Returns true for Succeeded, false for Failed.
func waitForPodDone(ctx context.Context, client kubernetes.Interface, ns, name string, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return false, fmt.Errorf("pod %s/%s did not complete within %v", ns, name, timeout)
		}

		pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("getting pod %s/%s: %w", ns, name, err)
		}

		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return true, nil
		case corev1.PodFailed:
			return false, nil
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func getPodIP(ctx context.Context, client kubernetes.Interface, ns, name string) (string, error) {
	pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("getting pod %s/%s: %w", ns, name, err)
	}
	if pod.Status.PodIP == "" {
		return "", fmt.Errorf("pod %s/%s has no IP assigned", ns, name)
	}
	return pod.Status.PodIP, nil
}

// ---------------------------------------------------------------------------
// NetworkPolicy builders
// ---------------------------------------------------------------------------

func buildDenyAllIngressPolicy(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      enforcementIngressPol,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "server"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{},
		},
	}
}

func applyDenyAllIngressPolicy(ctx context.Context, client kubernetes.Interface, ns string) error {
	_, err := client.NetworkingV1().NetworkPolicies(ns).Create(
		ctx, buildDenyAllIngressPolicy(ns), metav1.CreateOptions{})
	return err
}

func buildSelectiveAllowPolicy(ns string) *networkingv1.NetworkPolicy {
	proto := corev1.ProtocolTCP
	port := intstr.FromInt(enforcementTestPort)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      enforcementIngressPol,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "server"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"role": "allowed"},
					},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: &proto,
					Port:     &port,
				}},
			}},
		},
	}
}

func applySelectiveAllowPolicy(ctx context.Context, client kubernetes.Interface, ns string) error {
	pol := buildSelectiveAllowPolicy(ns)
	_, err := client.NetworkingV1().NetworkPolicies(ns).Update(ctx, pol, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.NetworkingV1().NetworkPolicies(ns).Create(ctx, pol, metav1.CreateOptions{})
	}
	return err
}

func buildDenyAllEgressPolicy(ns string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      enforcementEgressPol,
			Namespace: ns,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"role": "client"},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      []networkingv1.NetworkPolicyEgressRule{},
		},
	}
}

func applyDenyAllEgressPolicy(ctx context.Context, client kubernetes.Interface, ns string) error {
	_, err := client.NetworkingV1().NetworkPolicies(ns).Create(
		ctx, buildDenyAllEgressPolicy(ns), metav1.CreateOptions{})
	return err
}

func deleteNetworkPolicy(ctx context.Context, client kubernetes.Interface, ns, name string) error {
	err := client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
