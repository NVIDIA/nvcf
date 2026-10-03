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
	"strings"
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
	enforcementServerPod   = "netpol-server"
	enforcementIngressPol  = "netpol-test-ingress"
	enforcementEgressPol   = "netpol-test-egress"
)

// enforcementPropDelay is how long a policy change gets to reach the data
// plane before it is probed. A var so tests need not wait it out.
var enforcementPropDelay = 3 * time.Second

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

// orphanNamespaceTTL is the age beyond which a netpol-validation-* namespace
// is considered orphaned (deferred cleanup in a previous run didn't fire
// because the pod was SIGKILLed / OOMed / force-deleted) and is eligible for
// sweep. Set well above any legitimate run duration but below the CronJob
// interval so the next scheduled run will reclaim stale namespaces.
const orphanNamespaceTTL = 1 * time.Hour

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

	denyAll := runDenyAllIngressPhase(env, state)
	selective := runSelectiveAllowPhase(env, state)
	egressOK := runEgressPhase(env)

	log.Info("")
	switch {
	case denyAll == phaseFailed || selective == phaseFailed:
		// An observed failure decides the check whatever else did not run.
		ok := false
		state.EnforcementOK = &ok
		printError(log, "Network Policy Enforcement: VALIDATION FAILED")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: CNI does not enforce NetworkPolicies")
		state.Recommendations = append(state.Recommendations,
			"Verify your CNI plugin supports and enforces NetworkPolicies "+
				"(Calico, Cilium). Flannel does NOT support NetworkPolicies.")
	case denyAll == phaseNotRun || selective == phaseNotRun:
		// Leave the result unknown: the warnings name the call that failed.
		printWarning(log, "Network Policy Enforcement: not verified, because a phase could not run")
	case egressOK:
		ok := true
		state.EnforcementOK = &ok
		printSuccess(log, "Network Policy Enforcement: FULLY VERIFIED")
	default:
		ok := true
		state.EnforcementOK = &ok
		printSuccess(log, "Network Policy Enforcement: Ingress verified")
		printWarning(log, "Network Policy Enforcement: Egress not fully verified (non-critical)")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: Egress enforcement not verified (non-critical)")
	}
}

// phaseResult is what an enforcement phase established. Only probes that ran
// decide a phase: a policy or probe that could not be created, or a probe pod
// that never ran, says nothing about the CNI.
type phaseResult int

const (
	phaseNotRun phaseResult = iota
	phasePassed
	phaseFailed
)

// enforcementNotRun reports a step that could not run, naming it and the
// cause, and returns phaseNotRun.
func enforcementNotRun(log *logrus.Entry, state *ValidationState, msg string) phaseResult {
	printWarning(log, msg)
	state.Warnings = append(state.Warnings, "Network Policy Enforcement: status unknown ("+msg+")")
	return phaseNotRun
}

// observed maps a phase's outcome to its result.
func observed(ok bool) phaseResult {
	if ok {
		return phasePassed
	}
	return phaseFailed
}

func setupEnforcementEnv(
	ctx context.Context, client kubernetes.Interface,
	state *ValidationState, ns, image string, podTimeout time.Duration,
) (*enforcementEnv, bool) {
	log := state.Log
	log.Info("Phase 1: Setting up test environment")
	printInfo(log, fmt.Sprintf("Creating namespace %s", ns))

	if err := createTestNamespace(ctx, client, ns); err != nil {
		enforcementNotRun(log, state, fmt.Sprintf("could not create the test namespace %s: %v", ns, err))
		return nil, false
	}
	printSuccess(log, "Namespace created")

	printInfo(log, "Deploying server pod...")
	if err := createServerPod(ctx, client, ns, image); err != nil {
		enforcementNotRun(log, state, fmt.Sprintf("could not create the server pod in %s: %v", ns, err))
		return nil, false
	}

	// The IP comes from the read that saw the pod Ready. A second read only
	// adds a request that a transient error can fail.
	server, err := waitForPodReady(ctx, client, ns, enforcementServerPod, podTimeout)
	if err != nil {
		enforcementNotRun(log, state, fmt.Sprintf("the server pod did not become Ready: %v", err))
		return nil, false
	}
	serverIP := server.Status.PodIP
	if serverIP == "" {
		enforcementNotRun(log, state, fmt.Sprintf("the server pod %s/%s is Ready but has no IP",
			ns, enforcementServerPod))
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
		enforcementNotRun(log, state, fmt.Sprintf("the baseline client probe did not run: %v", err))
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
	if err != nil {
		enforcementNotRun(log, state, fmt.Sprintf("the baseline allowed-client probe did not run: %v", err))
		return false
	}
	if !allowedOK {
		printError(log, "Baseline: allowed-client cannot reach server — aborting")
		state.Warnings = append(state.Warnings,
			"Network Policy Enforcement: baseline connectivity broken")
		return false
	}
	printSuccess(log, "Baseline: allowed-client → server works")
	printSuccess(log, "Baseline connectivity verified — test harness is working")
	return true
}

func runDenyAllIngressPhase(env *enforcementEnv, state *ValidationState) phaseResult {
	log := env.log
	log.Info("")
	log.Info("Phase 3: Deny-all ingress enforcement")
	printInfo(log, "Applying deny-all ingress policy on server pod...")
	if err := observeErr(env.ctx, func(c context.Context) error {
		return applyDenyAllIngressPolicy(c, env.client, env.ns)
	}); err != nil {
		return enforcementNotRun(log, state, fmt.Sprintf("could not create the deny-all ingress policy: %v", err))
	}
	printSuccess(log, "Deny-all ingress policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy to propagate to data plane...",
		enforcementPropDelay))
	if !sleepCtx(env.ctx, enforcementPropDelay) {
		return enforcementNotRun(log, state, "the run ended before the deny-all probes")
	}

	printBlue(log, "Testing: client → server (expect: blocked)")
	clientReached, err := env.probe("client")
	if err != nil {
		return enforcementNotRun(log, state, fmt.Sprintf("the deny-all client probe did not run: %v", err))
	}
	printBlue(log, "Testing: allowed-client → server (expect: blocked)")
	allowedReached, err := env.probe("allowed")
	if err != nil {
		return enforcementNotRun(log, state, fmt.Sprintf("the deny-all allowed-client probe did not run: %v", err))
	}

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
	return observed(ok)
}

func runSelectiveAllowPhase(env *enforcementEnv, state *ValidationState) phaseResult {
	log := env.log
	log.Info("")
	log.Info("Phase 4: Selective allow rule validation")
	printInfo(log, "Applying selective allow policy (role=allowed only)...")
	if err := observeErr(env.ctx, func(c context.Context) error {
		return applySelectiveAllowPolicy(c, env.client, env.ns)
	}); err != nil {
		return enforcementNotRun(log, state, fmt.Sprintf("could not apply the selective allow policy: %v", err))
	}
	printSuccess(log, "Selective allow policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy update to propagate...",
		enforcementPropDelay))
	if !sleepCtx(env.ctx, enforcementPropDelay) {
		return enforcementNotRun(log, state, "the run ended before the selective allow probes")
	}

	printBlue(log, "Testing: client → server (expect: blocked)")
	clientReached, err := env.probe("client")
	if err != nil {
		return enforcementNotRun(log, state, fmt.Sprintf("the selective allow client probe did not run: %v", err))
	}
	printBlue(log, "Testing: allowed-client → server (expect: allowed)")
	allowedReached, err := env.probe("allowed")
	if err != nil {
		return enforcementNotRun(log, state,
			fmt.Sprintf("the selective allow allowed-client probe did not run: %v", err))
	}

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
	return observed(ok)
}

// egressSettleDelay gives the CNI time to program eBPF/iptables rules on a
// newly-created probe pod's veth before wget runs.
const egressSettleDelay = 3 // seconds

func runEgressPhase(env *enforcementEnv) bool {
	log := env.log
	log.Info("")
	log.Info("Phase 5: Egress policy enforcement")

	printInfo(log, "Removing ingress policy for clean egress test...")
	if err := observeErr(env.ctx, func(c context.Context) error {
		return deleteNetworkPolicy(c, env.client, env.ns, enforcementIngressPol)
	}); err != nil {
		printWarning(log, fmt.Sprintf("Could not remove ingress policy: %v", err))
	}
	if !sleepCtx(env.ctx, enforcementPropDelay) {
		return false
	}

	printInfo(log, "Verifying connectivity restored (clean slate)...")
	cleanSlate, err := env.probe("client")
	if err != nil {
		printWarning(log, fmt.Sprintf("Skipping egress test: the clean-slate probe did not run: %v", err))
		return false
	}
	if !cleanSlate {
		printWarning(log, "Client still cannot reach server after policy removal — stale state")
		printWarning(log, "Skipping egress test")
		return false
	}
	printSuccess(log, "Connectivity restored after policy removal")

	printInfo(log, "Applying deny-all egress policy on client pod...")
	if err := observeErr(env.ctx, func(c context.Context) error {
		return applyDenyAllEgressPolicy(c, env.client, env.ns)
	}); err != nil {
		printWarning(log, fmt.Sprintf("Could not create the deny-all egress policy: %v", err))
		return false
	}
	printSuccess(log, "Deny-all egress policy applied")
	printInfo(log, fmt.Sprintf("Waiting %v for policy to propagate...", enforcementPropDelay))
	if !sleepCtx(env.ctx, enforcementPropDelay) {
		return false
	}

	printBlue(log, "Testing: client → server (expect: blocked by egress)")
	clientReached, err := env.probeWithDelay("client", egressSettleDelay)
	if err != nil {
		printWarning(log, fmt.Sprintf("Egress not verified: the client probe did not run: %v", err))
		return false
	}
	printBlue(log, "Testing: allowed-client → server (expect: allowed, unaffected)")
	allowedReached, err := env.probeWithDelay("allowed", egressSettleDelay)
	if err != nil {
		printWarning(log, fmt.Sprintf("Egress not verified: the allowed-client probe did not run: %v", err))
		return false
	}

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

// sweepOrphanTestNamespaces lists all netpol-validation-* namespaces and
// deletes any whose age exceeds ttl. Used to reclaim leaks from prior runs
// that died before their deferred cleanup could fire (SIGKILL, OOM,
// force-delete, node failure). Namespaces younger than ttl are left alone
// in case they belong to a concurrent run.
func sweepOrphanTestNamespaces(
	ctx context.Context, log *logrus.Entry, client kubernetes.Interface, ttl time.Duration,
) {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	nsList, err := client.CoreV1().Namespaces().List(listCtx, metav1.ListOptions{
		LabelSelector: "app=netpol-validation",
	})
	if err != nil {
		log.Warnf("Orphan sweep: failed to list namespaces: %v", err)
		return
	}
	if len(nsList.Items) == 0 {
		return
	}

	cutoff := time.Now().Add(-ttl)
	deleted := 0
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if !strings.HasPrefix(ns.Name, "netpol-validation-") {
			continue
		}
		if ns.CreationTimestamp.After(cutoff) {
			continue // still within TTL — might be a concurrent run
		}
		delCtx, delCancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.CoreV1().Namespaces().Delete(delCtx, ns.Name, metav1.DeleteOptions{})
		delCancel()
		if err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Orphan sweep: failed to delete namespace %s: %v", ns.Name, err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		printInfo(log, fmt.Sprintf(
			"Orphan sweep: deleted %d stale netpol-validation-* namespace(s) older than %s",
			deleted, ttl))
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
				Resources:       enforcementResources(),
				SecurityContext: nodeToNodeSecurityContext(),
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
				Name:            "probe",
				Image:           image,
				Command:         []string{"sh", "-c", cmd},
				Resources:       enforcementResources(),
				SecurityContext: nodeToNodeSecurityContext(),
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
	if err := observeErr(ctx, func(c context.Context) error {
		_, err := client.CoreV1().Pods(ns).Create(c, pod, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			// An earlier attempt that timed out went through: the name is this
			// probe's own.
			return nil
		}
		return err
	}); err != nil {
		return false, fmt.Errorf("could not create the probe pod %s: %w", name, err)
	}
	defer func() {
		_ = client.CoreV1().Pods(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	}()

	succeeded, done, err := waitForPodDone(ctx, client, ns, name, timeout)
	if err != nil {
		return false, err
	}
	// Failed reads as blocked only when the probe ran: a pod the kubelet
	// refused, or one whose container never started, sent no traffic.
	if !succeeded && !containerTerminated(done) {
		return false, fmt.Errorf("probe pod %s/%s ended %s without running (%s)",
			ns, name, done.Status.Phase, done.Status.Reason)
	}
	return succeeded, nil
}

// containerTerminated reports whether any of pod's containers ran to an exit.
func containerTerminated(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			return true
		}
	}
	return false
}

// waitForPodReady polls until the named pod has the Ready condition, and
// returns the pod as that read saw it.
func waitForPodReady(
	ctx context.Context, client kubernetes.Interface, ns, name string, timeout time.Duration,
) (*corev1.Pod, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("pod %s/%s did not become ready within %v (last error: %w)",
					ns, name, timeout, lastErr)
			}
			return nil, fmt.Errorf("pod %s/%s did not become ready within %v", ns, name, timeout)
		}

		getCtx, cancel := attemptContext(ctx, deadline)
		pod, err := client.CoreV1().Pods(ns).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		switch {
		case err == nil:
			lastErr = nil
		case apierrors.IsNotFound(err):
			// Not created yet. The API answered, so an earlier transient
			// error no longer explains a timeout.
			lastErr = nil
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
			return nil, fmt.Errorf("getting pod %s/%s: %w", ns, name, err)
		default:
			// A slow or throttled Get is not the pod's answer. The per-attempt
			// cap turned an APF-queued Get into a hard failure of the whole
			// enforcement check, so retry it inside the deadline, as
			// waitForPodDone does.
			lastErr = err
		}
		if err == nil {
			if isPodReady(pod) {
				return pod, nil
			}
			if pod.Status.Phase == corev1.PodFailed {
				return nil, fmt.Errorf("pod %s/%s entered Failed phase", ns, name)
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// pollAttemptTimeout caps one API call inside a poll loop. The loops check
// their deadline only between attempts and the client sets no request timeout,
// so a single hung call could otherwise outlive the whole budget.
const pollAttemptTimeout = 10 * time.Second

// attemptContext bounds one poll attempt by pollAttemptTimeout and by the
// time left before deadline. An attempt that times out is an ordinary
// transient error to the caller's retry loop.
//
// The one-second floor is deliberate. waitForProbePods checks its deadline
// after each attempt, so its last attempt usually starts just past it (the
// deadline expires during the sleep). Without the floor that attempt is
// cancelled at once, and the probe reports the pods unobserved instead of
// classifying them, turning a real CNI fault into UNKNOWN. The loops that
// check the deadline first can overrun it by at most the floor.
func attemptContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, max(min(pollAttemptTimeout, time.Until(deadline)), time.Second))
}

// waitForPodDone polls until the named pod reaches Succeeded or Failed.
// Returns true for Succeeded, false for Failed, and the terminal pod, so a
// caller that needs its exit code does not fetch it again and risk losing the
// result to a transient error.
func waitForPodDone(
	ctx context.Context, client kubernetes.Interface, ns, name string, timeout time.Duration,
) (bool, *corev1.Pod, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return false, nil, fmt.Errorf("pod %s/%s did not complete within %v (last error: %w)",
					ns, name, timeout, lastErr)
			}
			return false, nil, fmt.Errorf("pod %s/%s did not complete within %v", ns, name, timeout)
		}

		getCtx, cancel := attemptContext(ctx, deadline)
		pod, err := client.CoreV1().Pods(ns).Get(getCtx, name, metav1.GetOptions{})
		cancel()
		switch {
		case err == nil:
			lastErr = nil
			switch pod.Status.Phase {
			case corev1.PodSucceeded:
				return true, pod, nil
			case corev1.PodFailed:
				return false, pod, nil
			}
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsNotFound(err):
			// Answers that cannot change inside the deadline.
			return false, nil, fmt.Errorf("getting pod %s/%s: %w", ns, name, err)
		default:
			// A 429 or apiserver blip is not the pod's result, so retry it
			// rather than spend one Get on the whole verdict.
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return false, nil, ctx.Err()
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
	if apierrors.IsAlreadyExists(err) {
		// The test namespace is this run's own, so an earlier attempt that
		// timed out created it.
		return nil
	}
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
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func deleteNetworkPolicy(ctx context.Context, client kubernetes.Interface, ns, name string) error {
	err := client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
