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
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

var smbVersionRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

// checkPrerequisites verifies basic cluster connectivity and gathers version info.
func checkPrerequisites(ctx context.Context, client kubernetes.Interface, state *ValidationState) error {
	log := state.Log
	printHeader(log, "Checking Prerequisites")

	sv, err := client.Discovery().ServerVersion()
	if err != nil {
		log.WithError(err).Error("Cannot connect to Kubernetes cluster")
		printError(log, "Cannot connect to Kubernetes cluster.")
		log.Error("╔═══════════════════════════════════════════════════════════╗")
		log.Errorf("\u2551              %s  %s%s  %s              \u2551",
			iconCross, VerdictLinePrefix, VerdictNotReady, iconCross)
		log.Error("╚═══════════════════════════════════════════════════════════╝")
		return fmt.Errorf("cluster not reachable")
	}

	printSuccess(log, "Connected to Kubernetes cluster")
	log.Info("")
	log.Info("Cluster Information:")
	state.K8sVersion = sv.GitVersion
	printInfo(log, fmt.Sprintf("  Kubernetes version: %s", state.K8sVersion))

	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	if err == nil {
		state.TotalNodes = strconv.Itoa(len(nodes.Items))
	} else {
		printWarning(log, readFailure("nodes", err))
		state.TotalNodes = "unknown"
	}
	printInfo(log, fmt.Sprintf("  Total nodes: %s", state.TotalNodes))

	// Report the container runtime(s) across nodes so operators can confirm
	// runtime compatibility during pre-flight. Diagnostic only — it does not
	// affect the verdict.
	if err == nil && len(nodes.Items) > 0 {
		state.ContainerRuntime = summarizeContainerRuntimes(nodes.Items)
		printInfo(log, fmt.Sprintf("  Container runtime: %s", state.ContainerRuntime))
	}

	return nil
}

// summarizeContainerRuntimes returns a deterministic, human-readable summary of
// the container runtime versions across nodes. When every node reports the same
// runtime it returns just that value (e.g. "containerd://1.7.27"); for a
// mixed-runtime cluster it lists each distinct runtime with its node count
// (e.g. "containerd://1.7.27 (2), cri-o://1.30.0 (1)"). A node with an empty
// ContainerRuntimeVersion is reported as "unknown".
func summarizeContainerRuntimes(nodes []corev1.Node) string {
	if len(nodes) == 0 {
		return "unknown"
	}

	counts := make(map[string]int, len(nodes))
	for i := range nodes {
		runtime := nodes[i].Status.NodeInfo.ContainerRuntimeVersion
		if runtime == "" {
			runtime = "unknown"
		}
		counts[runtime]++
	}

	runtimes := make([]string, 0, len(counts))
	for runtime := range counts {
		runtimes = append(runtimes, runtime)
	}
	sort.Strings(runtimes)

	if len(runtimes) == 1 {
		return runtimes[0]
	}

	parts := make([]string, 0, len(runtimes))
	for _, runtime := range runtimes {
		parts = append(parts, fmt.Sprintf("%s (%d)", runtime, counts[runtime]))
	}
	return strings.Join(parts, ", ")
}

// checkControlPlaneHealth verifies /readyz, in-cluster DNS, and service routing.
// Control-plane pod presence is informational only; /readyz is authoritative.
// NotReady worker nodes are Warning only (non-blocking).
func checkControlPlaneHealth(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Kubernetes Control Plane Health")
	podsHealthy := true   // Critical — flips cluster verdict
	nodesAllReady := true // Warning only — does not flip cluster verdict

	// ── 1. Canonical control plane health: /readyz ──
	// Use ServerVersion as the primary reachability gate (works with fake
	// clients in unit tests). Then attempt /readyz for a richer signal on
	// real clusters and distinguish three cases: (a) /readyz reports ready,
	// (b) /readyz reports not-ready, (c) /readyz not reachable (e.g. fake
	// client) — fall back to ServerVersion only.
	if _, verr := client.Discovery().ServerVersion(); verr == nil {
		reached, ready := probeReadyz(ctx, client)
		switch {
		case reached && ready:
			printSuccess(log, "API server /readyz reports healthy")
		case reached && !ready:
			printError(log, "API server /readyz reports not ready")
			podsHealthy = false
		default: // !reached — fall back to ServerVersion-only
			printSuccess(log, "API server is reachable (ServerVersion OK; /readyz unavailable)")
		}
	} else {
		printError(log, "API server is not ready")
		podsHealthy = false
	}

	// ── 2. Data-plane capabilities ──
	// Capability-based: probe what we actually depend on (DNS resolves,
	// service-routing reaches the API ClusterIP) rather than pod-name
	// patterns that vary per distribution. The pod-prefix detection
	// (CoreDNS vs kube-dns; kube-proxy vs Cilium eBPF vs OVN-Kubernetes
	// vs embedded-in-k3s) is kept only as a diagnostic line so the
	// operator can see WHAT is implementing each capability, but the
	// verdict comes from the capability probes themselves.
	log.Info("")
	log.Info("Data-Plane Capabilities:")

	if probeDNSFn(ctx) {
		printSuccess(log, "  DNS resolution: kubernetes.default.svc resolved")
	} else {
		printError(log, "  DNS resolution: failed to resolve kubernetes.default.svc")
		podsHealthy = false
	}

	if probeAPIServiceIPFn(ctx) {
		printSuccess(log, "  Service routing: kubernetes.default.svc reached via ClusterIP")
	} else {
		printError(log, "  Service routing: failed to reach kubernetes.default.svc")
		podsHealthy = false
	}

	// ── 3. Pod-presence diagnostics (informational only) ──
	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		printWarning(log, fmt.Sprintf("Could not list kube-system pods for diagnostics: %v", err))
	} else {
		log.Info("")
		log.Info("Diagnostics (informational — does not affect verdict):")

		if dnsProvider := detectDNSProvider(pods.Items); dnsProvider != "" {
			printInfo(log, fmt.Sprintf("  DNS provider: %s", dnsProvider))
		} else {
			printInfo(log, "  DNS provider: not recognised (capability probe above is authoritative)")
		}
		if routingImpl := detectServiceRoutingImpl(state.K8sVersion, pods.Items); routingImpl != "" {
			printInfo(log, fmt.Sprintf("  Service routing implementation: %s", routingImpl))
		} else {
			printInfo(log, "  Service routing implementation: not recognised "+
				"(capability probe above is authoritative)")
		}

		// Control-plane pods — diagnostic only, same as before. Tells the
		// operator whether the control plane runs as visible workloads
		// (self-hosted) or is hidden by a managed K8s provider (EKS, GKE,
		// AKS). /readyz from block 1 is the authoritative health signal.
		log.Info("")
		log.Info("Control Plane Pods (kube-system) [diagnostic only]:")
		controlPlanePods := []string{
			"kube-apiserver", "kube-controller-manager", "kube-scheduler", "etcd",
		}
		allHidden := true
		for _, prefix := range controlPlanePods {
			count := countRunningPods(pods.Items, prefix)
			if count > 0 {
				printSuccess(log, fmt.Sprintf("  %s: %d instance(s) running", prefix, count))
				allHidden = false
			} else {
				printInfo(log, fmt.Sprintf("  %s: not visible (managed by cloud provider?)", prefix))
			}
		}
		if allHidden {
			if provider := detectManagedClusterProvider(ctx, client); provider != "" {
				printInfo(log, fmt.Sprintf(
					"Detected managed control plane (%s) — control plane components are "+
						"managed by the cloud provider; API health is determined via /readyz above.",
					provider))
			} else {
				printInfo(log,
					"Control plane pods not visible — could be a managed control plane (no "+
						"recognised cloud-provider node label found) or a self-hosted cluster with "+
						"a degraded control plane. See /readyz result above for actual API health.")
			}
		}
	}

	// ── 4. Node status ──
	log.Info("")
	log.Info("Node Status:")
	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	nodesObserved := err == nil
	if err != nil {
		// Node readiness was not observed, which says nothing about the
		// control plane: /readyz and the probes above judge that.
		printWarning(log, "  "+readFailure("nodes", err))
		state.Warnings = append(state.Warnings, unknownWarning("Worker Nodes", "nodes", err))
		state.markUnobserved(CheckKeyWorkerNodesAllReady)
	} else if len(nodes.Items) > 0 {
		ready, notReady := 0, 0
		for i := range nodes.Items {
			n := &nodes.Items[i]
			isReady := false
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					isReady = true
					break
				}
			}
			if isReady {
				ready++
			} else {
				notReady++
			}
		}
		printInfo(log, fmt.Sprintf("  Ready nodes: %d", ready))
		if notReady > 0 {
			printWarning(log, fmt.Sprintf(
				"  NotReady nodes: %d (warning only — does not block readiness)", notReady))
			nodesAllReady = false
			state.NodesAllReady = false
			state.NotReadyNodes = notReady
			state.Warnings = append(state.Warnings, fmt.Sprintf(
				"Worker Nodes: %d NotReady (non-blocking; routine ops can proceed). "+
					"Run `kubectl get nodes` to identify the affected node(s).", notReady))
		}
	}

	// ── 5. Verdict ──
	log.Info("")
	switch {
	case podsHealthy && !nodesObserved:
		printWarning(log, "Control plane API & services healthy; worker node status not observed")
	case podsHealthy && nodesAllReady:
		printSuccess(log, "Control plane is healthy")
	case podsHealthy && !nodesAllReady:
		printWarning(log, "Control plane API & services healthy; some worker nodes are NotReady (non-blocking)")
	default: // !podsHealthy
		printError(log, "Some control plane components may need attention")
		state.ControlPlaneHealthy = false
		state.Recommendations = append(state.Recommendations,
			"Fix control plane issues: verify /readyz, in-cluster DNS resolution "+
				"(kubernetes.default.svc) and service routing "+
				"(https://kubernetes.default.svc/readyz). "+
				"See the 'Data-Plane Capabilities' and 'Diagnostics' sections above "+
				"for which probe failed and which provider/router was detected.")
	}
}

// isEmbeddedKubeProxyDistro returns true when the cluster's API-server
// version string identifies a distribution that embeds kube-proxy in the
// server binary instead of running it as a DaemonSet pod (k3s, k3d, rke2).
// On those distributions, the kube-proxy "pod missing" check is a false
// negative — the same code runs inside the server binary.
func isEmbeddedKubeProxyDistro(version string) bool {
	v := strings.ToLower(version)
	return strings.Contains(v, "+k3s") || strings.Contains(v, "+rke2")
}

// probeDNSFn and probeAPIServiceIPFn indirect the network probes so tests
// can swap them with stubs. Production code points them at the real probe
// functions in connectivity.go.
var (
	probeDNSFn          = probeInClusterDNS
	probeAPIServiceIPFn = probeKubernetesAPIServiceIP
)

// detectDNSProvider inspects kube-system pods and returns a short provider
// name (CoreDNS, kube-dns) when recognised. Diagnostic only; the authoritative
// DNS health signal comes from probeInClusterDNS.
func detectDNSProvider(pods []corev1.Pod) string {
	switch {
	case countRunningPods(pods, "coredns") > 0:
		return "CoreDNS"
	case countRunningPods(pods, "kube-dns") > 0:
		return "kube-dns"
	}
	return ""
}

// detectServiceRoutingImpl inspects K8s version and kube-system pods to
// identify the kube-proxy implementation (DaemonSet, k3s/rke2 embedded,
// Cilium, OVN-Kubernetes). Diagnostic only; probeKubernetesAPIServiceIP is authoritative.
func detectServiceRoutingImpl(k8sVersion string, pods []corev1.Pod) string {
	switch {
	case isEmbeddedKubeProxyDistro(k8sVersion):
		return "kube-proxy embedded in server binary (k3s/rke2)"
	case hasCiliumPods(pods):
		return "Cilium eBPF (kube-proxy replacement)"
	case countRunningPods(pods, "ovnkube-node") > 0:
		return "OVN-Kubernetes"
	case countRunningPods(pods, "kube-proxy") > 0:
		return "kube-proxy DaemonSet"
	}
	return ""
}

// hasCiliumPods returns true when at least one Running pod in the slice
// carries the canonical Cilium agent label k8s-app=cilium. Same signal
// used by checkNetworkPolicies for CNI detection.
func hasCiliumPods(pods []corev1.Pod) bool {
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodRunning &&
			pods[i].Labels["k8s-app"] == "cilium" {
			return true
		}
	}
	return false
}

// detectManagedClusterProvider scans node labels for well-known
// cloud-provider markers and returns a short provider name (EKS / GKE /
// AKS) when the cluster is positively identified as managed Kubernetes.
// Returns "" when nodes can't be listed or no managed-cluster label is
// found.
func detectManagedClusterProvider(ctx context.Context, client kubernetes.Interface) string {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 5})
	if err != nil {
		return ""
	}
	for i := range nodes.Items {
		labels := nodes.Items[i].Labels
		switch {
		case labels["eks.amazonaws.com/nodegroup"] != "":
			return "EKS"
		case labels["cloud.google.com/gke-nodepool"] != "":
			return "GKE"
		case labels["kubernetes.azure.com/agentpool"] != "":
			return "AKS"
		}
	}
	return ""
}

// probeReadyz performs GET /readyz on the API server and returns:
//   - reached=true, ready=true:  /readyz responded 2xx with body "ok"
//   - reached=true, ready=false: /readyz returned an HTTP 5xx (Kubernetes
//     signals unreadiness via 503) OR a non-"ok" body. We reached the
//     server; it explicitly told us it isn't ready.
//   - reached=false, ready=false: transport error, nil RESTClient, or panic
//     (fake clients may not implement RESTClient correctly). We could not
//     determine readiness — caller should fall back to ServerVersion.
func probeReadyz(ctx context.Context, client kubernetes.Interface) (reached, ready bool) {
	defer func() {
		if r := recover(); r != nil {
			reached, ready = false, false
		}
	}()
	rc := client.Discovery().RESTClient()
	if rc == nil {
		return false, false
	}
	raw, err := rc.Get().AbsPath("/readyz").DoRaw(ctx)
	if err != nil {
		// HTTP 5xx (typically 503 "shutting down" / "not yet ready") means
		// we reached the API server and it explicitly reported not-ready.
		// Any other error (DNS, TLS, connection refused, timeout) means we
		// could not reach it — fall back to ServerVersion-only at the
		// caller.
		var se *apierrors.StatusError
		if errors.As(err, &se) && se.ErrStatus.Code >= 500 && se.ErrStatus.Code < 600 {
			return true, false
		}
		return false, false
	}
	return true, strings.TrimSpace(string(raw)) == "ok"
}

// countRunningPods returns the number of Running pods whose name starts with
// the given prefix.
func countRunningPods(pods []corev1.Pod, prefix string) int {
	n := 0
	for i := range pods {
		p := &pods[i]
		if strings.HasPrefix(p.Name, prefix) && p.Status.Phase == corev1.PodRunning {
			n++
		}
	}
	return n
}

// checkWebhookSupport verifies that admission webhook APIs are available.
func checkWebhookSupport(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Webhook Support")
	supported := true

	log.Info("Admission Registration API:")
	hasMutating, hasValidating, err := discoverWebhookAPIs(ctx, client.Discovery())
	if err != nil {
		printWarning(log, readFailure("the admissionregistration.k8s.io/v1 API", err))
		state.Warnings = append(state.Warnings,
			unknownWarning("Admission Webhooks", "the admissionregistration.k8s.io/v1 API", err))
		state.markUnobserved(CheckKeyWebhooks)
		return
	}

	if hasMutating {
		printSuccess(log, "MutatingWebhookConfiguration API is available")
	} else {
		printError(log, "MutatingWebhookConfiguration API is not available")
		supported = false
	}
	if hasValidating {
		printSuccess(log, "ValidatingWebhookConfiguration API is available")
	} else {
		printError(log, "ValidatingWebhookConfiguration API is not available")
		supported = false
	}
	log.Info("")
	log.Info("Existing Webhooks:")
	mutList, err := client.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	mutCount := 0
	if err == nil {
		mutCount = len(mutList.Items)
	}
	valList, err := client.AdmissionregistrationV1().ValidatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	valCount := 0
	if err == nil {
		valCount = len(valList.Items)
	}
	printInfo(log, fmt.Sprintf("MutatingWebhookConfigurations: %d", mutCount))
	printInfo(log, fmt.Sprintf("ValidatingWebhookConfigurations: %d", valCount))
	log.Info("")
	if supported {
		printSuccess(log, "Cluster supports admission webhooks")
		state.WebhooksSupported = true
	} else {
		printError(log, "Cluster does not fully support admission webhooks")
		state.Recommendations = append(state.Recommendations,
			"Enable admission webhooks (MutatingAdmissionWebhook, ValidatingAdmissionWebhook)")
	}
}

// discoverWebhookAPIs reports which webhook configuration APIs are served. A
// group version the apiserver does not serve is an answer, not an error.
func discoverWebhookAPIs(
	ctx context.Context, disco discovery.DiscoveryInterface,
) (hasMutating, hasValidating bool, err error) {
	resources, err := serverResources(ctx, disco, "admissionregistration.k8s.io/v1")
	if apierrors.IsNotFound(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	for _, r := range resources.APIResources {
		switch r.Name {
		case "mutatingwebhookconfigurations":
			hasMutating = true
		case "validatingwebhookconfigurations":
			hasValidating = true
		}
	}
	return hasMutating, hasValidating, nil
}

// serverResources is ServerResourcesForGroupVersion with retries, bounded by
// ctx.
func serverResources(
	ctx context.Context, disco discovery.DiscoveryInterface, groupVersion string,
) (*metav1.APIResourceList, error) {
	return observe(ctx, func(c context.Context) (*metav1.APIResourceList, error) {
		return withContext(c, func() (*metav1.APIResourceList, error) {
			return disco.ServerResourcesForGroupVersion(groupVersion)
		})
	})
}

// checkNetworkPolicies verifies that the NetworkPolicy API is available and
// attempts to detect a known CNI plugin.
func checkNetworkPolicies(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Network Policy Support")
	supportsNetpol := false

	resources, err := serverResources(ctx, client.Discovery(), "networking.k8s.io/v1")
	if err != nil && !apierrors.IsNotFound(err) {
		printWarning(log, readFailure("the networking.k8s.io/v1 API", err))
		state.Warnings = append(state.Warnings,
			unknownWarning("Network Policies", "the networking.k8s.io/v1 API", err))
		state.markUnobserved(CheckKeyNetworkPoliciesSupport)
		return
	}

	found := false
	if err == nil {
		for _, r := range resources.APIResources {
			if r.Name == "networkpolicies" {
				found = true
				break
			}
		}
	}
	if !found {
		printError(log, "NetworkPolicy API is not available")
		state.Recommendations = append(state.Recommendations,
			"Ensure Kubernetes cluster supports networking.k8s.io API group")
		return
	}

	printSuccess(log, "NetworkPolicy API is available")
	log.Info("")
	log.Info("CNI Plugin Detection:")

	cniChecks := []struct {
		Name      string
		Namespace string
		Label     string
	}{
		{"Calico", "kube-system", "k8s-app=calico-node"},
		{"Cilium", "kube-system", "k8s-app=cilium"},
		{"Weave Net", "kube-system", "name=weave-net"},
		{"Antrea", "kube-system", "app=antrea"},
		{"Canal", "kube-system", "k8s-app=canal"},
	}

	// readErr keeps the last failed detection read: support that could not
	// be confirmed because nothing could be read is unknown, not unconfirmed.
	var readErr error
	for _, cni := range cniChecks {
		pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
			return client.CoreV1().Pods(cni.Namespace).List(c, metav1.ListOptions{LabelSelector: cni.Label})
		})
		if err != nil {
			readErr = err
		}
		if err == nil && len(pods.Items) > 0 {
			for i := range pods.Items {
				if pods.Items[i].Status.Phase == corev1.PodRunning {
					printSuccess(log, fmt.Sprintf("%s CNI detected (supports network policies)", cni.Name))
					supportsNetpol = true
					break
				}
			}
		}
		if supportsNetpol {
			break
		}
	}

	if !supportsNetpol {
		netpols, err := observe(ctx, func(c context.Context) (*networkingv1.NetworkPolicyList, error) {
			return client.NetworkingV1().NetworkPolicies("").List(c, metav1.ListOptions{Limit: 1})
		})
		if err != nil {
			readErr = err
		}
		switch {
		case err == nil && len(netpols.Items) > 0:
			printInfo(log, "Existing NetworkPolicies found in cluster")
			supportsNetpol = true
		case readErr != nil:
			printWarning(log, readFailure("the pods and NetworkPolicies that show CNI support", readErr))
			state.Warnings = append(state.Warnings, unknownWarning("Network Policies",
				"the pods and NetworkPolicies that show CNI support", readErr))
			state.markUnobserved(CheckKeyNetworkPoliciesSupport)
			return
		default:
			printWarning(log, "Could not detect a known CNI plugin with network policy support")
			printInfo(log, "Common CNI plugins checked: Calico, Cilium, Weave, Antrea, Canal")
		}
	}
	log.Info("")
	if supportsNetpol {
		printSuccess(log, "Cluster supports network policies")
		state.NetworkPoliciesSupported = true
	} else {
		printWarning(log, "Network policy support could not be confirmed")
		printInfo(log, "Network policies may still work if your CNI plugin supports them")
		printInfo(log, "Flannel and some cloud CNIs do NOT enforce network policies")
		state.Warnings = append(state.Warnings,
			"Network Policies: Could not confirm support - verify your CNI plugin supports them")
		state.Recommendations = append(state.Recommendations,
			"Verify your CNI plugin supports network policies (Calico, Cilium, etc.)")
	}
}

// checkSMBCSIDriver verifies the SMB CSI driver is installed and meets the
// minimum version requirement.
func checkSMBCSIDriver(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "SMB CSI Driver")
	const requiredVersion = "1.16.0"

	_, err := observe(ctx, func(c context.Context) (*storagev1.CSIDriver, error) {
		return client.StorageV1().CSIDrivers().Get(c, "smb.csi.k8s.io", metav1.GetOptions{})
	})
	if err != nil && !apierrors.IsNotFound(err) {
		printWarning(log, readFailure("CSIDriver smb.csi.k8s.io", err))
		state.Warnings = append(state.Warnings, unknownWarning("SMB CSI Driver", "CSIDriver smb.csi.k8s.io", err))
		state.markUnobserved(CheckKeySMBCSI)
		return
	}
	if err != nil {
		// SMB CSI is required only when the HelmSharedStorage feature flag
		// is enabled (model-cache backed by an in-cluster Samba server).
		// Surface as a Warning rather than an Error so the operator install
		// is not blocked for customers who do not use model caching. The
		// runtime health check in pkg/storage/smbcsidriver.go raises the
		// same condition at StatusLevelWarn — keep parity with that.
		printWarning(log, "SMB CSI Driver is NOT installed (non-blocking)")
		printInfo(log,
			fmt.Sprintf("SMB CSI Driver v%s+ is required only when NVCA model caching "+
				"(HelmSharedStorage feature flag) is enabled. Function-only workloads "+
				"do not need it.", requiredVersion))
		printInfo(log, "If you plan to enable model caching, install SMB CSI Driver via Helm:")
		log.Info("helm repo add csi-driver-smb https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts")
		log.Info("helm install csi-driver-smb csi-driver-smb/csi-driver-smb \\")
		log.Info("  --namespace kube-system \\")
		log.Infof("  --version v%s", requiredVersion)
		printInfo(log, "For more information: https://github.com/kubernetes-csi/csi-driver-smb")
		state.Warnings = append(state.Warnings,
			fmt.Sprintf("SMB CSI Driver v%s+ not installed. Required only when the "+
				"HelmSharedStorage feature flag is enabled. Non-blocking.", requiredVersion))
		return
	}

	printSuccess(log, "SMB CSI Driver is installed")
	log.Info("")
	log.Info("Version Check:")

	smbVersion := detectSMBVersion(ctx, client)
	if smbVersion != "" {
		printInfo(log, fmt.Sprintf("  Detected version: v%s", smbVersion))
		if versionGTE(smbVersion, requiredVersion) {
			printSuccess(log, fmt.Sprintf("  Version v%s meets minimum requirement (v%s+)", smbVersion, requiredVersion))
			state.SMBCSIDriverOK = true
		} else {
			printError(log, fmt.Sprintf("  Version v%s is below minimum requirement (v%s+)", smbVersion, requiredVersion))
			state.Recommendations = append(state.Recommendations,
				fmt.Sprintf("Upgrade SMB CSI Driver to v%s or higher", requiredVersion))
		}
	} else {
		printWarning(log, "  Could not determine SMB CSI Driver version")
		printInfo(log, fmt.Sprintf("  Please verify manually that version is v%s or higher", requiredVersion))
		state.SMBCSIDriverOK = true
		state.Recommendations = append(state.Recommendations,
			fmt.Sprintf("Verify SMB CSI Driver version is v%s or higher", requiredVersion))
	}
}

func detectSMBVersion(ctx context.Context, client kubernetes.Interface) string {
	namespaces := []string{"kube-system", "smb-csi", "csi-smb"}
	names := []string{"csi-smb-controller"}

	for _, ns := range namespaces {
		for _, name := range names {
			dep, err := client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			for _, c := range dep.Spec.Template.Spec.Containers {
				if m := smbVersionRe.FindStringSubmatch(c.Image); len(m) > 1 {
					return m[1]
				}
			}
		}

		deps, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app=csi-smb-controller",
		})
		if err != nil || len(deps.Items) == 0 {
			continue
		}
		for _, c := range deps.Items[0].Spec.Template.Spec.Containers {
			if m := smbVersionRe.FindStringSubmatch(c.Image); len(m) > 1 {
				return m[1]
			}
		}
	}
	return ""
}

// checkGPUResources inspects node GPU capacity and allocatable resources.
func checkGPUResources(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, GPUResourcesLabel)

	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	if err != nil {
		// No GPU count was read, which is not a count of zero.
		printWarning(log, readFailure("nodes", err))
		state.Warnings = append(state.Warnings, unknownWarning(GPUResourcesLabel, "nodes", err))
		state.markUnobserved(CheckKeyGPUResources)
		return
	}

	type gpuNode struct {
		Name        string
		Capacity    int64
		Allocatable int64
	}

	var gpuNodes []gpuNode
	var totalCapacity, totalAllocatable int64

	for i := range nodes.Items {
		n := &nodes.Items[i]
		capQ := n.Status.Capacity["nvidia.com/gpu"]
		allocQ := n.Status.Allocatable["nvidia.com/gpu"]
		gpuCap := capQ.Value()
		gpuAlloc := allocQ.Value()

		if gpuCap > 0 {
			gpuNodes = append(gpuNodes, gpuNode{
				Name:        n.Name,
				Capacity:    gpuCap,
				Allocatable: gpuAlloc,
			})
			totalCapacity += gpuCap
			totalAllocatable += gpuAlloc
		}
	}

	log.Info("GPU Node Summary:")
	printInfo(log, fmt.Sprintf("  Nodes with GPUs: %d", len(gpuNodes)))
	printInfo(log, fmt.Sprintf("  Total GPU capacity: %d", totalCapacity))
	printInfo(log, fmt.Sprintf("  Total GPU allocatable: %d", totalAllocatable))

	if totalCapacity > 0 {
		printInfo(log, fmt.Sprintf("  GPUs in use: %d", totalCapacity-totalAllocatable))
		log.Info("")
		log.Info("GPU Node Details:")
		for _, n := range gpuNodes {
			log.Infof("  %s: %d GPU(s) (allocatable: %d)", n.Name, n.Capacity, n.Allocatable)
		}
	}

	if totalCapacity == 0 {
		printWarning(log, "WARNING: No GPUs detected in the cluster!")
		printInfo(log, "This could mean:")
		printInfo(log, "  - No GPU nodes are present in the cluster")
		printInfo(log, "  - GPU Operator is not installed or not functioning")
		printInfo(log, "  - GPU drivers are not properly configured")
		state.Recommendations = append(state.Recommendations,
			"Add GPU nodes to the cluster or verify GPU Operator is functioning")
	} else {
		log.Info("")
		printSuccess(log, "GPU resources detected in cluster")
		state.GPUAvailable = true
	}
}

// checkGPUOperator verifies the GPU Operator is installed and running.
func checkGPUOperator(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "GPU Operator Status")

	const gpuOperatorNS = "gpu-operator"
	installed := false
	// readErr keeps a failed read: not finding the operator because nothing
	// could be read is unknown, not "not installed".
	var readErr error

	_, err := observe(ctx, func(c context.Context) (*corev1.Namespace, error) {
		return client.CoreV1().Namespaces().Get(c, gpuOperatorNS, metav1.GetOptions{})
	})
	if err != nil && !apierrors.IsNotFound(err) {
		readErr = err
	}
	if err == nil {
		printSuccess(log, fmt.Sprintf("GPU Operator namespace exists: %s", gpuOperatorNS))

		pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
			return client.CoreV1().Pods(gpuOperatorNS).List(c, metav1.ListOptions{})
		})
		if err != nil {
			readErr = err
		}
		if err == nil && len(pods.Items) > 0 {
			installed = true
			printSuccess(log, fmt.Sprintf("GPU Operator pods found: %d", len(pods.Items)))
			log.Info("")
			log.Info("GPU Operator Components:")
			for i := range pods.Items {
				p := &pods.Items[i]
				phase := p.Status.Phase
				if phase == corev1.PodRunning || phase == corev1.PodSucceeded {
					printSuccess(log, fmt.Sprintf("  %s: %s", p.Name, phase))
				} else {
					printWarning(log, fmt.Sprintf("  %s: %s", p.Name, phase))
				}
			}
			log.Info("")
			log.Info("ClusterPolicy Status:")
			printInfo(log, "  (ClusterPolicy CRD check requires dynamic client - skipped in lightweight mode)")
		}
	}

	if !installed {
		pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
			return client.CoreV1().Pods("").List(c, metav1.ListOptions{LabelSelector: "app=gpu-operator"})
		})
		if err != nil {
			readErr = err
		}
		if err == nil && len(pods.Items) > 0 {
			nsSet := make(map[string]bool)
			for i := range pods.Items {
				nsSet[pods.Items[i].Namespace] = true
			}
			nsList := make([]string, 0, len(nsSet))
			for ns := range nsSet {
				nsList = append(nsList, ns)
			}
			printInfo(log, fmt.Sprintf("GPU Operator found in namespace(s): %s", strings.Join(nsList, ", ")))
			installed = true
		}
	}

	if !installed && readErr != nil {
		printWarning(log, readFailure("the GPU Operator namespace and pods", readErr))
		state.Warnings = append(state.Warnings,
			unknownWarning("GPU Operator", "the GPU Operator namespace and pods", readErr))
		state.markUnobserved(CheckKeyGPUOperator)
		return
	}
	if !installed {
		// If GPUs are already usable (node capacity exposes nvidia.com/gpu),
		// GPU Operator is not required — the cluster is in Manual Instance
		// Configuration mode (or some other alternative GPU-exposure path).
		// Surface as a Warning instead of an Error and skip the install
		// recommendation that would mislead the operator.
		if state.GPUAvailable {
			printWarning(log, "GPU Operator is NOT installed (GPUs discovered via alternative mechanism — non-blocking)")
			printInfo(log,
				"This is expected for clusters registered with Manual Instance Configuration "+
					"or when GPU resources are exposed without GPU Operator. No action required.")
			state.Warnings = append(state.Warnings,
				"GPU Operator: not installed but GPUs are discoverable via alternative mechanism "+
					"(e.g. Manual Instance Configuration). Non-blocking.")
		} else {
			printError(log, "GPU Operator is NOT installed")
			printInfo(log, "To install GPU Operator with default configuration:")
			log.Info("# Add the NVIDIA Helm repository")
			log.Info("helm repo add nvidia https://helm.ngc.nvidia.com/nvidia")
			log.Info("helm repo update")
			log.Info("# Install GPU Operator with default driver and MIG disabled")
			log.Info("helm install gpu-operator nvidia/gpu-operator \\")
			log.Info("  --namespace gpu-operator \\")
			log.Info("  --create-namespace \\")
			log.Info("  --set mig.strategy=none \\")
			log.Info("  --set driver.enabled=true")
			printInfo(log, "For more information, see: https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html")
			state.Recommendations = append(state.Recommendations,
				"Install GPU Operator using the command above, or register the cluster "+
					"with Manual Instance Configuration if exposing GPUs by other means")
		}
	} else {
		printSuccess(log, "GPU Operator is installed")
		state.GPUOperatorInstalled = true
	}
}

// checkStorageClass verifies that a default StorageClass is present. NVCF
// workloads use PersistentVolumeClaims; without a default StorageClass those
// claims remain unbound and workloads fail to start. Critical for both
// control-plane (operator chart) and compute-plane (model cache), but surfaced
// here for the control-plane validator role.
func checkStorageClass(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Default StorageClass")

	classes, err := observe(ctx, func(c context.Context) (*storagev1.StorageClassList, error) {
		return client.StorageV1().StorageClasses().List(c, metav1.ListOptions{})
	})
	if err != nil {
		// Leave DefaultStorageClassOK nil (unknown): an API error is not
		// confirmation that no default StorageClass exists.
		printWarning(log, readFailure("StorageClasses", err))
		state.Warnings = append(state.Warnings, unknownWarning("Default StorageClass", "StorageClasses", err))
		return
	}

	// Collect every default rather than stopping at the first. Two classes
	// annotated is-default-class reject all PVCs on Kubernetes <1.26, and on
	// >=1.26 the apiserver picks the newest, which need not be the one listed
	// first here.
	var defaults []string
	for _, sc := range classes.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == annotationTrue ||
			sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == annotationTrue {
			defaults = append(defaults, sc.Name)
		}
	}

	if len(defaults) > 1 {
		const multiDefaultTolerated = "1.26.0"
		recommendation := "Exactly one StorageClass may be marked default. Clear the annotation on the extras with: " +
			"kubectl patch storageclass <name> -p " +
			`'{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"false"}}}'`

		// From 1.26 the apiserver resolves the ambiguity by picking the most
		// recently created default, so PVCs still bind. Failing the critical
		// check there would report NVCF-Not-Ready on a cluster that works,
		// which mid-CSI-migration clusters (gp2 plus gp3) hit routinely.
		if versionGTE(state.K8sVersion, multiDefaultTolerated) {
			msg := fmt.Sprintf(
				"Multiple default StorageClasses found (%s); Kubernetes >= %s binds PVCs with the newest, "+
					"but the extras should be cleared",
				strings.Join(defaults, ", "), multiDefaultTolerated)
			printWarning(log, msg)
			state.Warnings = append(state.Warnings, "Default StorageClass: "+msg)
			state.Recommendations = append(state.Recommendations, recommendation)
			ok := true
			state.DefaultStorageClassOK = &ok
			return
		}

		printError(log, fmt.Sprintf("Multiple default StorageClasses found (%s); PVCs may fail to bind",
			strings.Join(defaults, ", ")))
		state.Recommendations = append(state.Recommendations, recommendation)
		ok := false
		state.DefaultStorageClassOK = &ok
		return
	}

	var defaultClass string
	if len(defaults) == 1 {
		defaultClass = defaults[0]
	}

	if defaultClass == "" {
		printError(log, fmt.Sprintf("No default StorageClass found (%d classes present, none marked as default)",
			len(classes.Items)))
		state.Recommendations = append(state.Recommendations,
			"Mark a StorageClass as default with: "+
				"kubectl patch storageclass <name> -p "+
				"'{\"metadata\":{\"annotations\":{\"storageclass.kubernetes.io/is-default-class\":\"true\"}}}'")
		ok := false
		state.DefaultStorageClassOK = &ok
		return
	}

	printSuccess(log, fmt.Sprintf("Default StorageClass: %s", defaultClass))
	ok := true
	state.DefaultStorageClassOK = &ok
}

const (
	gatewayAPIGroup = "gateway.networking.k8s.io"
	// envoyGatewayNamespace is the namespace created by the Envoy Gateway Helm
	// chart by default. The stack exposes controllerNamespace with no default,
	// so an install can legitimately place it elsewhere: use
	// envoyGatewayNamespaceName rather than this constant directly.
	envoyGatewayNamespace = "envoy-gateway-system"
	// envoyGatewayNamespaceEnv overrides the namespace for installs that set
	// the stack's controllerNamespace to something other than the default.
	// Without it, both Envoy checks probe a namespace that does not exist and
	// report a live gateway as missing.
	envoyGatewayNamespaceEnv = "NVCF_ENVOY_GATEWAY_NAMESPACE"
	// nvcfGatewayNamesEnv lists the NVCF Gateways, comma-separated, each as
	// "namespace/name", replacing discovery from the NVCF routes; a bare name
	// is ignored with a warning. Envoy Gateway puts every Gateway's proxy in
	// its controller namespace by default, so the Envoy checks need the NVCF
	// Gateways to tell NVCF's proxies from another team's.
	nvcfGatewayNamesEnv = "NVCF_GATEWAY_NAMES"
	// Labels Envoy Gateway stamps on proxy Services and Deployments. A
	// per-Gateway proxy carries its Gateway's name and namespace; a
	// merged-gateways proxy serves every Gateway of a class and carries only
	// the class.
	owningGatewayNameLabel      = "gateway.envoyproxy.io/owning-gateway-name"
	owningGatewayNamespaceLabel = "gateway.envoyproxy.io/owning-gateway-namespace"
	owningGatewayClassLabel     = "gateway.envoyproxy.io/owning-gatewayclass"
	// envoyGatewayControllerSelector matches the controller Deployment's pods
	// only, excluding the data-plane proxies and certgen Job in the same namespace.
	envoyGatewayControllerSelector = "control-plane=envoy-gateway"
	// envoyGatewayControllerName is the controllerName of a GatewayClass that
	// Envoy Gateway runs. Only such a Gateway has an Envoy proxy to look for.
	envoyGatewayControllerName = "gateway.envoyproxy.io/gatewayclass-controller"
)

// gatewayRouteRequirements are the route types this repo's own charts apply,
// each at the exact apiVersion the manifests declare (deploy/helm/gateway-routes).
// The version is part of the requirement: a CRD served only under some other
// version still fails the Helm apply, so checking the bare resource name would
// pass a cluster the stack cannot actually install on.
var gatewayRouteRequirements = []struct{ groupVersion, resource string }{
	{gatewayAPIGroup + "/v1", "httproutes"},
	{gatewayAPIGroup + "/v1", "grpcroutes"},
	// TCPRoute ships only in the Gateway API experimental channel, but the
	// chart renders one by default (routes.grpc.enabled is true), so a
	// standard-channel install genuinely cannot apply the stack.
	{gatewayAPIGroup + "/v1alpha2", "tcproutes"},
	{gatewayAPIGroup + "/v1beta1", "referencegrants"},
}

// gatewayOptionalRouteRequirements are route types the charts render only when
// an opt-in feature is enabled, so their absence is not a reason to fail a
// cluster that never turns that feature on. Reported by the non-critical
// checkGatewayRoutes with the feature named, rather than by the critical CRD
// check: udproute-llm-worker.yaml and referencegrant-llm-worker.yaml render a
// UDPRoute whenever routes.llmWorker.enabled, which defaults to false.
var gatewayOptionalRouteRequirements = []struct{ groupVersion, resource, enabledBy string }{
	{gatewayAPIGroup + "/v1alpha2", "udproutes", "nvcfGatewayRoutes.routes.llmWorker.enabled"},
}

// gatewayControllerResources are created by the Envoy Gateway chart rather than
// by this repo, so which version it picks is not ours to pin. Presence under
// any served version is all this check can assert.
var gatewayControllerResources = []string{"gatewayclasses", "gateways"}

// gatewayAPISurface is what the apiserver serves under gateway.networking.k8s.io.
type gatewayAPISurface struct {
	// byGroupVersion is keyed "<groupVersion>/<resource>", for example
	// "gateway.networking.k8s.io/v1/httproutes".
	byGroupVersion map[string]bool
	// anyVersion holds resource names served under at least one version.
	anyVersion map[string]bool
}

func (s gatewayAPISurface) hasPair(groupVersion, resource string) bool {
	return s.byGroupVersion[groupVersion+"/"+resource]
}

// discoverGatewayAPIResources walks every served version of
// gateway.networking.k8s.io and records what it finds, keeping the version so
// callers can require an exact pair where the charts pin one.
func discoverGatewayAPIResources(ctx context.Context, client kubernetes.Interface) (gatewayAPISurface, error) {
	surface := gatewayAPISurface{
		byGroupVersion: make(map[string]bool),
		anyVersion:     make(map[string]bool),
	}
	disco := client.Discovery()
	groups, err := observe(ctx, func(c context.Context) (*metav1.APIGroupList, error) {
		return withContext(c, disco.ServerGroups)
	})
	if err != nil {
		return surface, fmt.Errorf("listing API groups: %w", err)
	}
	for _, g := range groups.Groups {
		if g.Name != gatewayAPIGroup {
			continue
		}
		for _, v := range g.Versions {
			resources, err := serverResources(ctx, disco, v.GroupVersion)
			if err != nil {
				// The group exists, so a failure here is an API problem, not an
				// absent resource. Swallowing it would leave the surface
				// incomplete and report the CRDs as missing on a healthy cluster.
				return surface, fmt.Errorf("listing resources for %s: %w", v.GroupVersion, err)
			}
			for _, r := range resources.APIResources {
				surface.byGroupVersion[v.GroupVersion+"/"+r.Name] = true
				surface.anyVersion[r.Name] = true
			}
		}
	}
	return surface, nil
}

// checkGatewayAPICRDsIn verifies that the Gateway API CRD set is installed and
// registers all four required resource types. Without these CRDs neither the
// Gateway controller nor nvcf-cli can create routing objects.
// It judges an already discovered surface, so Run pays for discovery once.
func checkGatewayAPICRDsIn(state *ValidationState, surface gatewayAPISurface, err error) {
	log := state.Log
	printHeader(log, "Gateway API CRDs")

	if err != nil {
		// Leave the pointer nil: discovery failure is not evidence the CRDs
		// are absent, and this row is critical.
		printWarning(log, readFailure("the Gateway API resources", err))
		state.Warnings = append(state.Warnings,
			unknownWarning("Gateway API CRDs", "the Gateway API resources", err))
		return
	}

	var missing []string
	for _, r := range gatewayControllerResources {
		if !surface.anyVersion[r] {
			missing = append(missing, r)
		}
	}
	for _, req := range gatewayRouteRequirements {
		if !surface.hasPair(req.groupVersion, req.resource) {
			missing = append(missing, req.groupVersion+"/"+req.resource)
		}
	}
	if len(missing) > 0 {
		printError(log, fmt.Sprintf("Gateway API CRDs missing resources: %s", strings.Join(missing, ", ")))
		// Name the channel rather than pointing back at the installer: TCPRoute
		// and UDPRoute exist only in the experimental channel, so an operator
		// who ran the standard-channel manifest needs to know that is the
		// difference, not to re-run the install that just failed.
		state.Recommendations = append(state.Recommendations,
			"Install the Gateway API CRDs from the experimental channel, which is the only one carrying "+
				"TCPRoute and UDPRoute: kubectl apply -f "+
				"https://github.com/kubernetes-sigs/gateway-api/releases/download/<version>/experimental-install.yaml")
		ok := false
		state.GatewayAPICRDsOK = &ok
		return
	}

	printSuccess(log, fmt.Sprintf("Gateway API CRDs installed: %s plus the route types the stack applies",
		strings.Join(gatewayControllerResources, ", ")))
	ok := true
	state.GatewayAPICRDsOK = &ok
}

// checkEnvoyGateway verifies the Envoy Gateway controller is installed and has
// at least one running pod in its namespace (envoyGatewayNamespaceName). Without a
// running gateway controller, Gateway and HTTPRoute objects are never reconciled
// and no traffic reaches NVCF services.
func checkEnvoyGateway(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Envoy Gateway")

	envoyNS := envoyGatewayNamespaceName()
	_, err := observe(ctx, func(c context.Context) (*corev1.Namespace, error) {
		return client.CoreV1().Namespaces().Get(c, envoyNS, metav1.GetOptions{})
	})
	if err != nil {
		// Only NotFound is evidence that Envoy is absent. A 403 or an apiserver
		// 500 means we never observed it, so leave the pointer nil and warn,
		// matching every sibling control-plane check.
		if !apierrors.IsNotFound(err) {
			printWarning(log, readFailure("namespace "+envoyNS, err))
			state.Warnings = append(state.Warnings, unknownWarning("Envoy Gateway", "namespace "+envoyNS, err))
			return
		}
		msg := fmt.Sprintf("Envoy Gateway namespace %s not found", envoyNS)
		printError(log, msg)
		// Envoy is non-critical, so without a warning the run prints the green
		// "meets all requirements" banner above this failing row.
		state.Warnings = append(state.Warnings, "Envoy Gateway: "+msg+
			"; set clusterValidator.envoyGatewayNamespace if it is installed elsewhere")
		state.Recommendations = append(state.Recommendations,
			"Install Envoy Gateway via the NVCF self-managed stack (nvcf-cli up) or "+
				"helm install eg oci://docker.io/envoyproxy/gateway-helm -n "+envoyNS+" --create-namespace")
		ok := false
		state.EnvoyGatewayOK = &ok
		return
	}

	// Select on the controller label: the same namespace also holds the
	// envoy-<ns>-<gw>-<hash> data-plane proxies and the certgen Job pod, and
	// counting those lets a dead controller pass.
	pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
		return client.CoreV1().Pods(envoyNS).List(c, metav1.ListOptions{LabelSelector: envoyGatewayControllerSelector})
	})
	if err != nil {
		// Same reasoning as the namespace Get above: a List failure is not
		// evidence that no controller is running.
		resource := "the Envoy Gateway controller pods in " + envoyNS
		printWarning(log, readFailure(resource, err))
		state.Warnings = append(state.Warnings, unknownWarning("Envoy Gateway", resource, err))
		return
	}

	// Require Ready, not Running: .status.phase stays Running throughout
	// CrashLoopBackOff, so a crash-looping controller counts as healthy.
	ready := 0
	for i := range pods.Items {
		if isPodReady(&pods.Items[i]) {
			ready++
		}
	}
	log.Infof("  Controller pods in %s: %d total, %d ready", envoyNS, len(pods.Items), ready)

	if ready == 0 {
		msg := fmt.Sprintf("No Ready Envoy Gateway controller pods in %s (%d found)",
			envoyNS, len(pods.Items))
		printError(log, msg)
		// printSummary chooses the banner from len(state.Warnings), so without
		// this a CrashLooping controller printed the green "meets all
		// requirements" box above its own failing row.
		state.Warnings = append(state.Warnings, "Envoy Gateway: "+msg)
		ok := false
		state.EnvoyGatewayOK = &ok
		return
	}

	printSuccess(log, fmt.Sprintf("Envoy Gateway: %d controller pod(s) Ready in %s", ready, envoyNS))
	ok := true
	state.EnvoyGatewayOK = &ok
}

// checkGatewayRoutesIn verifies that the route CR types NVCF creates are
// registered with the apiserver. It judges an already discovered surface only: it does not list
// route objects, so it cannot tell whether any route actually exists.
//
// Non-critical: route CR types are installed by nvcf up and are expected to
// be absent on a fresh cluster before install.
func checkGatewayRoutesIn(state *ValidationState, surface gatewayAPISurface, err error) {
	log := state.Log
	printHeader(log, "Gateway Route CR Types")

	if err != nil {
		// Leave the pointer nil: a discovery failure is not evidence that the
		// route CR types are absent.
		printWarning(log, readFailure("the Gateway API resources", err))
		state.Warnings = append(state.Warnings,
			unknownWarning("Gateway Route CR Types", "the Gateway API resources", err))
		return
	}

	// Only the opt-in route types are assessed here. The mandatory set is the
	// critical checkGatewayAPICRDs' job, and duplicating it would pay a second
	// discovery walk to compute a row that can never differ from that one.
	var missing, present []string
	for _, req := range gatewayOptionalRouteRequirements {
		pair := req.groupVersion + "/" + req.resource
		if surface.hasPair(req.groupVersion, req.resource) {
			present = append(present, pair)
			continue
		}
		missing = append(missing, pair+" (needed when "+req.enabledBy+")")
	}

	if len(missing) > 0 {
		printWarning(log, fmt.Sprintf("Optional route CR types not registered: %s",
			strings.Join(missing, ", ")))
		state.Warnings = append(state.Warnings,
			"Gateway Route CR Types: optional types absent ("+strings.Join(missing, ", ")+
				"); install the Gateway API experimental channel before enabling those routes")
		ok := false
		state.GatewayRoutesOK = &ok
		return
	}

	printSuccess(log, "Optional route CR types registered: "+strings.Join(present, ", "))
	ok := true
	state.GatewayRoutesOK = &ok
}

// checkExternalLoadBalancerFor performs a passive check that the NVCF Gateways'
// proxy Services have an external address from a load balancer controller
// (cloud LB, MetalLB, etc.).
//
// Envoy Gateway puts every Gateway's proxy Service in its own namespace by
// default, including other teams', so the check first decides which Gateways
// are NVCF's (gatewayOwnership, shared with Tier-1 so the rows agree) and judges only their Services. When that
// cannot be decided it falls back to the whole namespace and says so.
//
// Non-critical: the passive form only detects an existing LB service; it does
// not create a probe service, so absence means either no LB service exists yet
// or no LB controller is installed.
func checkExternalLoadBalancerFor(
	ctx context.Context, client kubernetes.Interface, own *gatewayOwnership, state *ValidationState,
) {
	log := state.Log
	printHeader(log, "External Load Balancer")

	envoyNS := envoyGatewayNamespaceName()
	gateways, source, discoveryErr := own.gateways, own.source, own.err
	own.reportInvalid(log, state)
	switch {
	case discoveryErr != nil:
		printWarning(log, fmt.Sprintf("Could not determine which Gateways belong to NVCF: %v", discoveryErr))
	case len(gateways) > 0:
		printInfo(log, fmt.Sprintf("  NVCF Gateways (%s): %s", source, gateways))
	case state.PostInstall:
		// Installed, yet nothing names an NVCF Gateway: NVCF's own proxy may
		// be among the Services, so none of them can vouch for it.
		discoveryErr = errNoNVCFGatewaysPostInstall
	}

	// Default Envoy Gateway mode puts the proxy Services in the controller
	// namespace; GatewayNamespace mode puts them beside each Gateway. Search
	// both so either mode is covered once the Gateway namespaces are known.
	namespaces := append([]string{envoyNS}, gateways.namespaces(envoyNS)...)
	var services []corev1.Service
	var unread []string
	var unreadErrs []error
	for _, ns := range namespaces {
		list, err := observe(ctx, func(c context.Context) (*corev1.ServiceList, error) {
			return client.CoreV1().Services(ns).List(c, metav1.ListOptions{})
		})
		if err != nil {
			// Keep going: what the other namespaces show still counts, and a
			// pending NVCF Service fails the row whatever was not read.
			printWarning(log, readFailure("Services in "+ns, err))
			unread = append(unread, fmt.Sprintf("%s (%v)", ns, err))
			unreadErrs = append(unreadErrs, err)
			continue
		}
		services = append(services, list.Items...)
	}
	gap := ""
	if len(unread) > 0 {
		gap = "could not read Services in " + readFailures(unread, unreadErrs)
	}

	if len(gateways) > 0 {
		// Classes matter only to a merged-gateways proxy, so the Gateways are
		// listed only when one is present.
		var classes map[string][]string
		var classErr error
		for i := range services {
			if l := services[i].Labels; l[owningGatewayNameLabel] == "" && l[owningGatewayClassLabel] != "" {
				classes, classErr = own.gatewayClasses(ctx)
				break
			}
		}
		judgeNVCFGatewayServices(log, state, gateways, classes, classErr, services, gap)
		return
	}
	judgeUnattributedServices(log, state, services, discoveryErr, gap)
}

// errNoNVCFGatewaysPostInstall stands in for a failed ownership lookup when the
// control plane is installed but no route or setting names an NVCF Gateway.
var errNoNVCFGatewaysPostInstall = errors.New("no NVCF routes found although the control plane is installed")

// lbResult is one LoadBalancer Service with its assigned address.
type lbResult struct {
	name      string
	namespace string
	addr      string
}

// lbAddress returns the first IP or hostname the LB controller assigned.
func lbAddress(svc *corev1.Service) string {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			return ing.IP
		}
		if ing.Hostname != "" {
			return ing.Hostname
		}
	}
	return ""
}

// judgeNVCFGatewayServices is the strict path: every NVCF Gateway must have a
// proxy Service, and every one exposed through a LoadBalancer must have an
// address. Other Gateways' Services are ignored. gap names the namespaces
// whose Services could not be read, or is empty.
func judgeNVCFGatewayServices(
	log *logrus.Entry, state *ValidationState, gateways gatewaySet,
	classes map[string][]string, classErr error, services []corev1.Service, gap string,
) {
	var found []lbResult
	var pending []string
	seen := map[string]bool{}
	mergedProxies := 0
	for i := range services {
		svc := &services[i]
		if svc.Labels[owningGatewayNameLabel] == "" && svc.Labels[owningGatewayClassLabel] != "" {
			mergedProxies++
		}
		entries := gateways.entriesForProxy(svc.Labels, classes)
		if len(entries) == 0 {
			continue
		}
		for _, e := range entries {
			seen[e] = true
		}
		entry := strings.Join(entries, ", ")
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			// An EnvoyProxy can expose a Gateway as NodePort or ClusterIP on
			// purpose; that Gateway has no external LB to check.
			printInfo(log, fmt.Sprintf("  %s/%s (Gateway %s) is %s, not a LoadBalancer",
				svc.Namespace, svc.Name, entry, svc.Spec.Type))
			continue
		}
		if addr := lbAddress(svc); addr != "" {
			found = append(found, lbResult{svc.Name, svc.Namespace, addr})
		} else {
			pending = append(pending, svc.Namespace+"/"+svc.Name)
		}
	}
	var missing []string
	for _, entry := range gateways.sorted() {
		if !seen[entry] {
			missing = append(missing, entry)
		}
	}

	// A merged-gateways proxy may be serving the "missing" Gateways, but
	// without their classes it cannot be attributed; and a proxy Service may
	// sit in a namespace that could not be read. Either way they are unknown
	// rather than missing. An NVCF proxy observed without an address still
	// fails the row: that is a failure whatever the unobserved ones show.
	var undecided []string
	if len(missing) > 0 && classErr != nil && mergedProxies > 0 {
		undecided = append(undecided, fmt.Sprintf("the merged-gateways proxy could not be attributed: %v", classErr))
	}
	if len(missing) > 0 && gap != "" {
		undecided = append(undecided, gap)
	}
	switch {
	case len(undecided) > 0 && len(pending) == 0:
		msg := fmt.Sprintf("no proxy Service seen for %s, and %s", strings.Join(missing, ", "),
			strings.Join(undecided, "; and "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "External Load Balancer: status unknown ("+msg+")")
		return
	case len(undecided) > 0:
		// The pending Service decides the row; the gap is still reported.
		msg := fmt.Sprintf("not assessed for %s: %s", strings.Join(missing, ", "), strings.Join(undecided, "; and "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "External Load Balancer: "+msg)
	case gap != "":
		// Every NVCF Gateway's Service was seen, so the unread namespaces do
		// not decide the row, but they are still reported.
		printWarning(log, gap)
		state.Warnings = append(state.Warnings, "External Load Balancer: "+gap)
	}

	if (len(missing) > 0 && len(undecided) == 0) || len(pending) > 0 {
		var problems []string
		if len(missing) > 0 && len(undecided) == 0 {
			problems = append(problems, fmt.Sprintf("no proxy Service found for Gateway(s) %s; "+
				"check the Gateway exists and Envoy Gateway provisioned it", strings.Join(missing, ", ")))
		}
		if len(pending) > 0 {
			problems = append(problems, strings.Join(pending, ", ")+
				" have no external address; check the load balancer controller and its address pool")
		}
		msg := strings.Join(problems, ". ")
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "External Load Balancer: "+msg+".")
		ok := false
		state.ExternalLBOK = &ok
		return
	}

	if len(found) == 0 {
		// Every NVCF Gateway is exposed some other way, so there is no LB to
		// verify. Unknown rather than a failure or a pass.
		msg := "no NVCF Gateway is exposed through a LoadBalancer Service"
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "External Load Balancer: status unknown ("+msg+")")
		return
	}

	printLBSuccess(log, found)
	ok := true
	state.ExternalLBOK = &ok
}

// judgeUnattributedServices is the fallback when the NVCF Gateways are not
// known. A failed discovery is unknown whatever the Services show: NVCF's own
// proxies may sit in a Gateway namespace that was not listed, or not use a
// LoadBalancer at all, so neither a foreign address nor a foreign pending
// Service says anything about NVCF's. With a known-empty route set (typically
// before nvcf-cli up), nothing addressed fails and a pending Service beside an
// addressed one only warns.
func judgeUnattributedServices(
	log *logrus.Entry, state *ValidationState, services []corev1.Service, discoveryErr error, gap string,
) {
	var found []lbResult
	var pending []string
	for i := range services {
		svc := &services[i]
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		if addr := lbAddress(svc); addr != "" {
			found = append(found, lbResult{svc.Name, svc.Namespace, addr})
		} else {
			pending = append(pending, svc.Namespace+"/"+svc.Name)
		}
	}

	if discoveryErr != nil {
		msg := fmt.Sprintf("could not determine which Gateways belong to NVCF (%v), so %d addressed and "+
			"%d pending LoadBalancer Service(s) cannot be attributed", discoveryErr, len(found), len(pending))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "External Load Balancer: status unknown ("+msg+")")
		switch {
		case errors.Is(discoveryErr, errNoNVCFGatewaysPostInstall):
			// The routes were read and none is NVCF's, so more permissions
			// would not change the result.
			state.Recommendations = append(state.Recommendations,
				"Check that the gateway routes release is installed, or set clusterValidator.gatewayNames "+
					"(env "+nvcfGatewayNamesEnv+").")
		case apierrors.IsForbidden(discoveryErr):
			state.Recommendations = append(state.Recommendations,
				"Grant the cluster-validator ServiceAccount get and list on gateway.networking.k8s.io "+
					"httproutes, grpcroutes, tcproutes and udproutes, or set clusterValidator.gatewayNames "+
					"(env "+nvcfGatewayNamesEnv+").")
		default:
			// Throttling or an apiserver fault: the grants are not the cause.
			state.Recommendations = append(state.Recommendations,
				"Re-run once the apiserver is healthy, or set clusterValidator.gatewayNames "+
					"(env "+nvcfGatewayNamesEnv+") so the NVCF Gateways need not be discovered.")
		}
		return
	}

	if gap != "" {
		if len(found) == 0 {
			// An addressed Service may be in a namespace that was not read.
			msg := fmt.Sprintf("%d pending LoadBalancer Service(s) seen, and %s", len(pending), gap)
			printWarning(log, msg)
			state.Warnings = append(state.Warnings, "External Load Balancer: status unknown ("+msg+")")
			return
		}
		state.Warnings = append(state.Warnings, "External Load Balancer: "+gap)
	}

	if len(found) == 0 && len(pending) > 0 {
		printWarning(log, fmt.Sprintf("LoadBalancer Service(s) awaiting an external address: %s",
			strings.Join(pending, ", ")))
		state.Warnings = append(state.Warnings,
			"External Load Balancer: "+strings.Join(pending, ", ")+
				" have no external address. Check the load balancer controller and its address pool.")
		ok := false
		state.ExternalLBOK = &ok
		return
	}
	if len(found) == 0 {
		// No LoadBalancer Service exists at all, which is normal before
		// nvcf-cli up: there is nothing to judge the LB controller by.
		printWarning(log, "No Service of type LoadBalancer exists")
		printInfo(log, "  This may indicate: no LB controller is installed (MetalLB, cloud LB), "+
			"or no LoadBalancer Service exists yet (normal before nvcf-cli up)")
		state.Warnings = append(state.Warnings,
			"External Load Balancer: status unknown (no Service of type LoadBalancer exists yet, so whether a "+
				"load balancer controller assigns addresses was not observed). Verify one is installed.")
		return
	}

	if len(pending) > 0 {
		printWarning(log, fmt.Sprintf("LoadBalancer Service(s) awaiting an external address: %s",
			strings.Join(pending, ", ")))
		state.Warnings = append(state.Warnings,
			"External Load Balancer: "+strings.Join(pending, ", ")+" have no external address. "+
				"No NVCF routes were found to tell whether they belong to NVCF; set "+
				"clusterValidator.gatewayNames (env "+nvcfGatewayNamesEnv+") to check the NVCF Gateways.")
	}
	printLBSuccess(log, found)
	ok := true
	state.ExternalLBOK = &ok
}

func printLBSuccess(log *logrus.Entry, found []lbResult) {
	printSuccess(log, fmt.Sprintf("%d LoadBalancer Service(s) with external address:", len(found)))
	for _, svc := range found {
		printInfo(log, fmt.Sprintf("  %s/%s -> %s", svc.namespace, svc.name, svc.addr))
	}
}

const (
	nodeToNodeTestPort = 19999
	// nodeToNodeNSPrefix names a per-run probe namespace. Running in a
	// dedicated namespace rather than "default" keeps a default-deny
	// NetworkPolicy, istio-injection, an SCC rejecting the runAsUser, or a
	// registry allowlist from surfacing as an overlay fault. The random suffix
	// keeps concurrent runs from deleting each other's namespace.
	nodeToNodeNSPrefix       = "nvcf-n2n-validation-"
	nodeToNodeDSName         = "nvcf-n2n-server"
	nodeToNodeCheckerName    = "nvcf-n2n-checker"
	nodeToNodeActiveDeadline = int64(180)
	nodeToNodeCheckerTimeout = 90 * time.Second
	// nodeToNodeImageEnv is the dedicated probe image override. An operator
	// debugging the node-to-node row has no reason to look under an
	// enforcement-test setting, so this one is named for the check it serves.
	nodeToNodeImageEnv = "NVCF_N2N_PROBE_IMAGE"
	// Exit codes of the checker script. Only the first is network evidence.
	nodeToNodeUnreachableExit = 3
	nodeToNodeNoNetcatExit    = 4
	// orphanN2NNamespaceTTL is the minimum age before a leftover
	// nvcf-n2n-validation-* namespace is swept. Must exceed the sum of the
	// DaemonSet and checker timeouts to avoid racing a concurrent run.
	orphanN2NNamespaceTTL = 10 * time.Minute
)

// nodeToNodeProbeImage resolves the probe image. The dedicated
// NVCF_N2N_PROBE_IMAGE override wins, then the enforcement.testImage the
// sibling NetworkPolicy probe uses, then the public busybox default. Without an
// override, an air-gapped or registry-mirrored cluster ImagePullBackOffs on
// every DaemonSet pod.
func nodeToNodeProbeImage(cfg *NetworkCheckConfig) string {
	if img := strings.TrimSpace(os.Getenv(nodeToNodeImageEnv)); img != "" {
		return img
	}
	if cfg != nil && cfg.Enforcement != nil && cfg.Enforcement.TestImage != "" {
		return cfg.Enforcement.TestImage
	}
	return enforcementDefaultImg
}

// createNodeToNodeNamespace creates the per-run probe namespace. The labels
// are what sweepOrphanN2NNamespaces matches on, and are deliberately distinct
// from the netpol-validation labels so the two sweeps cannot cross-delete.
func createNodeToNodeNamespace(ctx context.Context, client kubernetes.Interface, ns string) error {
	_, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
				"app.kubernetes.io/component":  "n2n-probe",
			},
		},
	}, metav1.CreateOptions{})
	return err
}

// legacyNodeToNodeNamespace is where validator versions before the per-run
// probe namespace created their DaemonSet. Their orphans there are still
// reclaimed, since those versions can be deployed alongside this one.
const legacyNodeToNodeNamespace = "default"

// sweepLegacyOrphanN2NDaemonSets reclaims probe DaemonSets stranded in
// "default" by an older validator that was killed before its cleanup ran.
// The per-run namespace sweep cannot see those: they predate the namespace.
// Without this they persist indefinitely, one probe pod per node.
func sweepLegacyOrphanN2NDaemonSets(
	ctx context.Context, log *logrus.Entry, client kubernetes.Interface, ttl time.Duration,
) {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	dsList, err := client.AppsV1().DaemonSets(legacyNodeToNodeNamespace).List(listCtx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=nvcf-cluster-validator,app.kubernetes.io/component=n2n-server",
	})
	if err != nil {
		// Say so: a silent return here leaves legacy probe DaemonSets running
		// one pod per node until some later sweep happens to succeed, with
		// nothing in the log to explain why. Matches sweepOrphanN2NNamespaces.
		log.Warnf("N2N legacy orphan sweep: failed to list DaemonSets in %s: %v",
			legacyNodeToNodeNamespace, err)
		return
	}
	if len(dsList.Items) == 0 {
		return
	}

	cutoff := time.Now().Add(-ttl)
	grace := int64(0)
	deleted := 0
	for i := range dsList.Items {
		ds := &dsList.Items[i]
		// Require the name too, as the namespace sweep does: these labels are
		// public constants and this deletes objects in a shared namespace.
		// Prefix, not equality: every version that created these named them
		// nodeToNodeDSName + "-" + suffix, so an equality check matches nothing
		// and the sweep silently reclaims none of the orphans it exists for.
		if !strings.HasPrefix(ds.Name, nodeToNodeDSName+"-") {
			continue
		}
		if ds.CreationTimestamp.After(cutoff) {
			continue // still within TTL; might be a concurrent run
		}
		delCtx, delCancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.AppsV1().DaemonSets(legacyNodeToNodeNamespace).Delete(delCtx, ds.Name,
			metav1.DeleteOptions{GracePeriodSeconds: &grace})
		delCancel()
		if err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("N2N legacy orphan sweep: failed to delete DaemonSet %s: %v", ds.Name, err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		printInfo(log, fmt.Sprintf("N2N legacy orphan sweep: deleted %d stale server DaemonSet(s) in %s older than %s",
			deleted, legacyNodeToNodeNamespace, ttl))
	}
}

// sweepOrphanN2NNamespaces deletes any nvcf-n2n-validation-* namespaces older
// than ttl, taking the DaemonSet and checker pod inside with them. These are
// left behind when the validator process is killed with SIGKILL (OOM,
// force-delete, node failure) before the deferred cleanup fires. Namespaces
// younger than ttl are skipped in case they belong to a concurrent run.
func sweepOrphanN2NNamespaces(ctx context.Context, log *logrus.Entry, client kubernetes.Interface, ttl time.Duration) {
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	nsList, err := client.CoreV1().Namespaces().List(listCtx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=nvcf-cluster-validator,app.kubernetes.io/component=n2n-probe",
	})
	if err != nil {
		log.Warnf("N2N orphan sweep: failed to list namespaces: %v", err)
		return
	}

	cutoff := time.Now().Add(-ttl)
	deleted := 0
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		// Belt and braces: the label selector should be sufficient, but require
		// the name prefix too so a mislabelled namespace is never deleted.
		if !strings.HasPrefix(ns.Name, nodeToNodeNSPrefix) {
			continue
		}
		if ns.CreationTimestamp.After(cutoff) {
			continue // still within TTL; might be a concurrent run
		}
		delCtx, delCancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.CoreV1().Namespaces().Delete(delCtx, ns.Name, metav1.DeleteOptions{})
		delCancel()
		if err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("N2N orphan sweep: failed to delete namespace %s: %v", ns.Name, err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		printInfo(log, fmt.Sprintf("N2N orphan sweep: deleted %d stale probe namespace(s) older than %s", deleted, ttl))
	}
}

// checkNodeToNode verifies overlay-network connectivity across the nodes that
// can run work, using a DaemonSet-based probe. A server DaemonSet runs on every
// Ready, uncordoned node that is not fenced off (nodeToNodeTargets); a checker
// pod on one of them, picked at random each run, connects to the server pod
// IP on each of the others.
//
// The topology is a single-source star, not a full mesh: one run proves the
// checker's node reaches every other node, which catches a dead overlay and
// most single-node isolation. It does not prove node[i] reaches node[j] for
// two other nodes, nor the reverse direction; picking the checker's node at
// random lets successive runs cover different paths.
//
// This check creates a namespace, a DaemonSet, and a pod, so the ServiceAccount
// must hold create/delete on all three. Any rejection of those creates, and a
// probe pod that cannot pull its image or start its container, leaves the
// result unknown rather than failing the overlay: none of them is evidence that
// node-to-node traffic is broken. Only a checker that ran and could not
// connect, or a probe pod whose latest sandbox event is a failed creation,
// fails the check.
//
// Critical: broken overlay means NVCF services on different nodes cannot
// communicate, causing cascade failures across every API call.
func checkNodeToNode(ctx context.Context, client kubernetes.Interface, state *ValidationState, image string) {
	log := state.Log
	printHeader(log, "Node-to-Node Communication")

	// notObserved leaves the pointer nil: only a probe that ran, or a probe pod
	// the pod network could not bring up, is evidence about the overlay.
	notObserved := func(msg, recommendation string) {
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Node-to-Node: status unknown ("+msg+")")
		if recommendation != "" {
			state.Recommendations = append(state.Recommendations, recommendation)
		}
	}

	// Reclaim DaemonSets orphaned by prior runs killed before their deferred
	// cleanup fired (SIGKILL, OOM, node failure).
	sweepOrphanN2NNamespaces(ctx, log, client, orphanN2NNamespaceTTL)
	sweepLegacyOrphanN2NDaemonSets(ctx, log, client, orphanN2NNamespaceTTL)

	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	if err != nil {
		printWarning(log, readFailure("nodes", err))
		state.Warnings = append(state.Warnings, unknownWarning("Node-to-Node", "nodes", err))
		return
	}

	// The probe tolerates every taint, so it reaches control-plane and GPU
	// nodes, and is pinned to the nodes it is expected on: Ready, not
	// cordoned, and not fenced off. Tolerating every taint would otherwise
	// also place it on NotReady and draining nodes, where it cannot run and
	// its pod cannot be deleted, so the probe namespace would stay
	// Terminating until the node returns.
	probeNodes, skippedNodes := nodeToNodeTargets(nodes.Items)
	if len(skippedNodes) > 0 {
		printInfo(log, fmt.Sprintf("  Not probed: %s", strings.Join(skippedNodes, ", ")))
	}

	// Zero and one are different answers. No usable node at all means the
	// cluster cannot place work and nothing was observed, so the result stays
	// unknown. Exactly one means there is no cross-node path to exercise, so
	// the requirement is moot rather than unobserved. Either way the warnings
	// list says why the row has no value, as the metrics contract requires.
	if len(probeNodes) == 0 {
		printWarning(log, "No Ready, schedulable nodes; node-to-node overlay not observed")
		state.Warnings = append(state.Warnings,
			"Node-to-Node: status unknown (no Ready, schedulable nodes)")
		return
	}
	if len(probeNodes) == 1 {
		printInfo(log, "  1 Ready, schedulable node; node-to-node check not applicable")
		state.NodeToNodeNotApplicable = "single schedulable node, no cross-node path"
		state.Warnings = append(state.Warnings,
			"Node-to-Node: not applicable (one Ready, schedulable node, so there is no cross-node path to probe)")
		return
	}

	suffix := rand.String(6)
	dsName := nodeToNodeDSName + "-" + suffix
	checkerName := nodeToNodeCheckerName + "-" + suffix
	// checkerPods holds every checker pod this run created, for the cleanup.
	var checkerPods []string
	dsLabels := map[string]string{
		"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
		"app.kubernetes.io/component":  "n2n-server",
		"app.kubernetes.io/instance":   suffix,
	}

	ns := nodeToNodeNSPrefix + suffix
	// AlreadyExists is an error too: the deferred cleanup deletes this
	// namespace, so it must be one this run created.
	if err := createNodeToNodeNamespace(ctx, client, ns); err != nil {
		notObserved(fmt.Sprintf("could not create the probe namespace %s: %v", ns, err), "")
		return
	}

	// Deleting the namespace removes the DaemonSet and checker pod with it, but
	// delete them first so a namespace stuck terminating does not strand the
	// probe pods on every node. Each delete gets its own budget: sharing one
	// let a slow DaemonSet delete spend the namespace delete's time, leaking
	// the namespace until a later run's sweep.
	defer func() {
		grace := int64(0)
		opts := metav1.DeleteOptions{GracePeriodSeconds: &grace}
		del := func(f func(context.Context) error) error {
			delCtx, cancel := context.WithTimeout(context.Background(), nodeToNodeDeleteTimeout)
			defer cancel()
			return f(delCtx)
		}
		_ = del(func(c context.Context) error { return client.AppsV1().DaemonSets(ns).Delete(c, dsName, opts) })
		for _, name := range checkerPods {
			_ = del(func(c context.Context) error { return client.CoreV1().Pods(ns).Delete(c, name, opts) })
		}
		if err := del(func(c context.Context) error {
			return client.CoreV1().Namespaces().Delete(c, ns, metav1.DeleteOptions{})
		}); err != nil && !apierrors.IsNotFound(err) {
			log.Warnf("Failed to clean up probe namespace %s: %v", ns, err)
		}
	}()

	ds := buildNodeToNodeDaemonSet(dsName, ns, dsLabels, image, probeNodes)
	if _, err := observe(ctx, func(c context.Context) (*appsv1.DaemonSet, error) {
		return client.AppsV1().DaemonSets(ns).Create(c, ds, metav1.CreateOptions{})
	}); err != nil {
		// Any create error means the probe never ran, which says nothing
		// about the overlay. Classifying by status code split one cause
		// across two verdicts: RBAC, a ResourceQuota and Gatekeeper return
		// 403, Kyverno 400, a ValidatingAdmissionPolicy 422 and a fail-closed
		// webhook 500 or 503.
		notObserved(fmt.Sprintf("could not create the probe DaemonSet in %s: %v", ns, err), "")
		return
	}

	log.Infof("  Waiting for probe pods on %d node(s)...", len(probeNodes))
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: dsLabels})
	pods, err := waitForProbePods(ctx, client, ns, selector, probeNodes, nodeToNodeDSTimeout)
	if err != nil {
		var unobserved *probeNotObservedError
		if errors.As(err, &unobserved) {
			notObserved("probe pods were not observed: "+unobserved.reason, "")
			return
		}
		notObserved(fmt.Sprintf("could not read probe pod status: %v", err), "")
		return
	}

	sandboxes, eventsErr := probeSandboxEvents(ctx, client, ns)
	outcome := classifyProbeNodes(pods, probeNodes, sandboxes, eventsErr)
	if len(outcome.networkFaults) > 0 {
		// A pod the node scheduled but could not give an address to is the
		// fault this check exists for, whatever the other nodes did.
		printError(log, fmt.Sprintf("Probe pods got no pod IP on %d node(s): %s",
			len(outcome.networkFaults), strings.Join(outcome.networkFaults, ", ")))
		state.Recommendations = append(state.Recommendations,
			"Check the CNI on the listed nodes: the pod was scheduled but its sandbox, and so its pod IP, "+
				"was never created. Look for FailedCreatePodSandBox events and the CNI daemon's and "+
				"container runtime's logs there.")
		ok := false
		state.NodeToNodeOK = &ok
		return
	}
	if len(outcome.running) < 2 {
		rec := ""
		if outcome.pullFailed {
			rec = nodeToNodeImageRecommendation
		}
		notObserved("probe pods did not start on two nodes: "+strings.Join(outcome.gaps, ", "), rec)
		return
	}
	if len(outcome.gaps) > 0 {
		// Probe the nodes whose pods started; a node the probe itself could not
		// use is a coverage gap, not a verdict on the overlay.
		msg := fmt.Sprintf("not probed on %d node(s): %s", len(outcome.gaps), strings.Join(outcome.gaps, ", "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Node-to-Node: "+msg)
		if outcome.pullFailed {
			state.Recommendations = append(state.Recommendations, nodeToNodeImageRecommendation)
		}
	}

	run, unobserved := runNodeToNodeChecker(ctx, client, ns, checkerName, image, outcome.running, &checkerPods)
	if unobserved != "" {
		notObserved(unobserved, "")
		return
	}
	checkerNode, succeeded, checker := run.node, run.succeeded, run.pod
	log.Infof("  Checker on %s, server pods on %d other node(s)", checkerNode, run.targets)

	if succeeded {
		printSuccess(log, fmt.Sprintf("Node-to-node overlay verified: %s -> %d node(s) reachable on port %d",
			checkerNode, run.targets, nodeToNodeTestPort))
		ok := true
		state.NodeToNodeOK = &ok
		return
	}
	// The checker is pinned with NodeName, so the scheduler's resource check
	// is skipped and a full node rejects it at kubelet admission (OutOfcpu,
	// OutOfpods) with phase Failed. Only the script's own exit code says the
	// connection itself failed.
	if why := checkerFailureCause(checker); why != "" {
		notObserved("checker pod failed without testing the overlay: "+why, "")
		return
	}
	printError(log, fmt.Sprintf("Checker on %s could not reach one or more server pods (port %d)",
		checkerNode, nodeToNodeTestPort))
	printInfo(log, "  Possible causes: CNI overlay misconfiguration, host firewall rules, "+
		"or cloud security group rules blocking inter-node pod traffic")
	state.Recommendations = append(state.Recommendations,
		"Check host firewall and security groups between nodes. "+
			"Verify the CNI overlay (VXLAN, Geneve, etc.) is not blocked across all nodes.")
	ok := false
	state.NodeToNodeOK = &ok
}

// checkerRun is what the node-to-node checker pod reported.
type checkerRun struct {
	node      string
	targets   int
	succeeded bool
	pod       *corev1.Pod
}

// runNodeToNodeChecker runs the checker pod on one node to dial the server pod
// on each of the others, and returns its result, or why no result was read.
// The node is picked at random so successive runs test different paths. A
// node whose kubelet refuses the checker (a node at its pod limit) is passed
// over for the next: a refusal says nothing about the overlay, and always
// picking the same node made it UNKNOWN on every run. created collects every
// checker pod made, for the cleanup.
func runNodeToNodeChecker(
	ctx context.Context, client kubernetes.Interface, ns, namePrefix, image string, servers []corev1.Pod,
	created *[]string,
) (checkerRun, string) {
	var refusals []string
	for _, i := range rand.Perm(len(servers)) {
		if len(refusals) == nodeToNodeCheckerAttempts {
			break
		}
		run := checkerRun{node: servers[i].Spec.NodeName}
		var targetIPs []string
		for j := range servers {
			if servers[j].Spec.NodeName != run.node && servers[j].Status.PodIP != "" {
				targetIPs = append(targetIPs, servers[j].Status.PodIP)
			}
		}
		run.targets = len(targetIPs)
		name := fmt.Sprintf("%s-%d", namePrefix, len(refusals))
		*created = append(*created, name)
		pod := buildNodeToNodeCheckerPod(name, ns, run.node, targetIPs, image)
		if _, err := observe(ctx, func(c context.Context) (*corev1.Pod, error) {
			return client.CoreV1().Pods(ns).Create(c, pod, metav1.CreateOptions{})
		}); err != nil {
			// As for the DaemonSet: a checker that was never created says
			// nothing about the overlay.
			return run, fmt.Sprintf("could not create the checker pod in %s: %v", ns, err)
		}
		// An error here means no result was read: the checker never finished,
		// or its status could not be fetched. A connection failure is reported
		// as a Failed phase, not an error. The terminal pod comes back with
		// the result, so the exit code is read from it rather than fetched
		// again: a 429 on a second Get lost a real connection failure to
		// UNKNOWN.
		var err error
		run.succeeded, run.pod, err = waitForPodDone(ctx, client, ns, name, nodeToNodeCheckerTimeout)
		if err != nil {
			return run, fmt.Sprintf("checker pod did not report a result: %v", err)
		}
		if run.succeeded || !checkerRefused(run.pod) {
			return run, ""
		}
		refusals = append(refusals, fmt.Sprintf("%s (%s)", run.node, run.pod.Status.Reason))
	}
	return checkerRun{}, "the kubelet refused the checker pod on " + strings.Join(refusals, ", ")
}

// nodeToNodeDeleteTimeout bounds each probe cleanup delete.
const nodeToNodeDeleteTimeout = 20 * time.Second

// nodeToNodeDSTimeout is how long the probe pods get to start. A var so tests
// that leave a node's pod down on purpose need not wait it out.
var nodeToNodeDSTimeout = 2 * time.Minute

// nodeToNodeCheckerAttempts bounds how many nodes the checker pod is tried on
// when the kubelet refuses it.
const nodeToNodeCheckerAttempts = 3

// nodeFencingTaints mark a node that is NotReady, being drained or removed,
// or without a working pod network. A probe pod there tests nothing, and on a
// node that is gone it cannot be deleted. The probe tolerates every taint so
// that it reaches control-plane and GPU nodes, which leaves this list to keep
// it off these.
var nodeFencingTaints = map[string]bool{
	corev1.TaintNodeNotReady:                         true,
	corev1.TaintNodeUnreachable:                      true,
	corev1.TaintNodeUnschedulable:                    true,
	corev1.TaintNodeNetworkUnavailable:               true,
	corev1.TaintNodeOutOfService:                     true,
	"node.cloudprovider.kubernetes.io/uninitialized": true,
	"node.cilium.io/agent-not-ready":                 true,
	"karpenter.sh/disrupted":                         true,
	"karpenter.sh/disruption":                        true,
	"ToBeDeletedByClusterAutoscaler":                 true,
	"virtual-kubelet.io/provider":                    true,
}

// nodeToNodeTargets returns the nodes a probe pod is expected to start on, a
// Ready node that is not cordoned and not fenced off, and a note for each node
// left out.
func nodeToNodeTargets(nodes []corev1.Node) (targets, skipped []string) {
	for i := range nodes {
		n := &nodes[i]
		switch why := nodeFenced(n); {
		case n.Spec.Unschedulable:
			skipped = append(skipped, n.Name+" (cordoned)")
		case !isNodeReady(n):
			skipped = append(skipped, n.Name+" (NotReady)")
		case why != "":
			skipped = append(skipped, n.Name+" ("+why+")")
		default:
			targets = append(targets, n.Name)
		}
	}
	return targets, skipped
}

// nodeFenced returns why a Ready node is still not one to probe, or "".
func nodeFenced(n *corev1.Node) string {
	if n.DeletionTimestamp != nil {
		return "being deleted"
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeNetworkUnavailable && c.Status == corev1.ConditionTrue {
			return "network unavailable"
		}
	}
	for _, t := range n.Spec.Taints {
		if nodeFencingTaints[t.Key] {
			return "tainted " + t.Key
		}
	}
	return ""
}

func isNodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// probeSnapshotMaxAge is how old the last pod list may be when the wait ends
// and still be classified. An older one shows the pods as they were, not as
// they are: listed right after the DaemonSet was created, every pod would
// read as a node that never networked it.
var probeSnapshotMaxAge = 15 * time.Second

// waitForProbePods waits until every expected node has a Running probe pod
// with an IP, and returns the pods from the last successful list either way:
// the caller decides what a straggler means. A list that fails after earlier
// ones succeeded does not discard what they showed, since the final attempt
// usually starts just past the deadline, but only a list from close to the
// deadline is returned. Never listing at all, a stale last list, or a
// permission error leaves nothing to classify.
func waitForProbePods(
	ctx context.Context, client kubernetes.Interface, ns, selector string, expected []string, timeout time.Duration,
) ([]corev1.Pod, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastPods []corev1.Pod
	var listedAt time.Time
	for {
		listCtx, cancel := attemptContext(ctx, deadline)
		pods, err := client.CoreV1().Pods(ns).List(listCtx, metav1.ListOptions{LabelSelector: selector})
		cancel()
		switch {
		case err == nil:
			listedAt, lastPods = time.Now(), pods.Items
			if len(classifyProbeNodes(lastPods, expected, nil, errEventsNotRead).running) == len(expected) {
				return lastPods, nil
			}
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
			return nil, err
		default:
			// Retry transient errors inside the deadline: client-go defaults to
			// 5 QPS and this run issues many LISTs.
			lastErr = err
		}
		if time.Now().After(deadline) {
			if listedAt.IsZero() {
				return nil, &probeNotObservedError{reason: fmt.Sprintf("listing probe pods: %v", lastErr)}
			}
			if age := time.Since(listedAt); age > probeSnapshotMaxAge {
				return nil, &probeNotObservedError{reason: fmt.Sprintf(
					"the last successful probe pod list is %s old; listing has failed since: %v",
					age.Round(time.Second), lastErr)}
			}
			return lastPods, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// probeNotObservedError reports a probe that produced no evidence about the
// overlay.
type probeNotObservedError struct {
	reason string
}

func (e *probeNotObservedError) Error() string { return e.reason }

// probeImagePullReasons are the kubelet waiting reasons for an image the node
// could not fetch. The first attempt reports ErrImagePull before the backoff
// starts, and a malformed override reports InvalidImageName.
var probeImagePullReasons = map[string]bool{
	"ErrImagePull":      true,
	"ImagePullBackOff":  true,
	"InvalidImageName":  true,
	"ErrImageNeverPull": true,
}

// probeContainerStartReasons are waiting reasons for a pulled probe container
// that could not run. None involves the pod network: a CNI fault leaves the pod
// in ContainerCreating with a FailedCreatePodSandBox event, which stays a
// failure.
var probeContainerStartReasons = map[string]bool{
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"CrashLoopBackOff":           true,
}

const nodeToNodeImageRecommendation = "Node-to-node probe image could not be pulled. Set " +
	"clusterValidator.nodeToNodeProbeImage (env " + nodeToNodeImageEnv + ") to a busybox-compatible " +
	"image the nodes can pull, for example a copy in your registry mirror."

// probeOutcome sorts the expected nodes by what their probe pod showed.
type probeOutcome struct {
	// running holds one Running pod with an IP per node that has one.
	running []corev1.Pod
	// networkFaults are nodes whose pod was scheduled and never got an IP for
	// no other visible reason: only the node or the pod network explains it.
	networkFaults []string
	// gaps are nodes the probe could not use for a reason of its own:
	// no pod, not scheduled, still pulling, image or container errors, or a
	// kubelet rejection. They say nothing about the overlay.
	gaps       []string
	pullFailed bool
}

// errEventsNotRead stands in for the events while the probe pods are still
// being waited on, when only the Running ones matter.
var errEventsNotRead = errors.New("not read yet")

// classifyProbeNodes decides, per expected node, what its probe pod says.
// sandboxes holds what each pod's events say about its sandbox, and eventsErr
// why they could not be read, or nil. The kubelet creates the sandbox, and
// with it the pod's network, before it pulls or starts anything, but
// publishes the pod IP only once that sync returns. So a pod with no IP whose
// events show a pull or a container start has a working pod network, and one
// whose latest sandbox event is a failed creation does not. Anything else,
// including no sandbox event at all or events that could not be read, is
// undecided rather than a fault: silence is not evidence about the network.
func classifyProbeNodes(
	pods []corev1.Pod, expected []string, sandboxes map[string]sandboxEvidence, eventsErr error,
) probeOutcome {
	byNode := map[string][]*corev1.Pod{}
	for i := range pods {
		p := &pods[i]
		if node := probePodNode(p); node != "" {
			byNode[node] = append(byNode[node], p)
		}
	}
	var out probeOutcome
	for _, node := range expected {
		candidates := byNode[node]
		var pick *corev1.Pod
		for _, p := range candidates {
			if p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
				pick = p
				break
			}
			if pick == nil || p.CreationTimestamp.After(pick.CreationTimestamp.Time) {
				pick = p
			}
		}
		if pick == nil {
			// The DaemonSet controller could not create it: Pod Security
			// Admission, a pod quota or a webhook. Its FailedCreate event says.
			out.gaps = append(out.gaps, node+": no probe pod was created")
			continue
		}
		if pick.Status.Phase == corev1.PodRunning && pick.Status.PodIP != "" {
			out.running = append(out.running, *pick)
			continue
		}
		reason := probePodBlocker(pick)
		switch {
		case reason != "":
		case pick.Spec.NodeName == "":
			reason = "not scheduled"
		case pick.Status.PodIP != "":
			reason = "networked but not yet Running"
		case sandboxes[pick.Name] == sandboxCreated:
			reason = "sandbox created, pod IP not yet reported (image pull or start in progress)"
		case sandboxes[pick.Name] == sandboxFailed:
			out.networkFaults = append(out.networkFaults, node)
			continue
		case eventsErr != nil:
			reason = fmt.Sprintf("pod events could not be read (%v), so whether its sandbox exists is undecided",
				eventsErr)
		default:
			reason = "no sandbox event yet, so whether its pod network works is undecided"
		}
		if probeImagePullReasons[reason] {
			out.pullFailed = true
		}
		out.gaps = append(out.gaps, node+": "+reason)
	}
	return out
}

// probePodNode is the node a DaemonSet pod is for. An unbound pod has no
// spec.nodeName yet, but the DaemonSet controller pins it to its node with a
// metadata.name node-affinity field, so an unschedulable pod still names it.
func probePodNode(p *corev1.Pod) string {
	if p.Spec.NodeName != "" {
		return p.Spec.NodeName
	}
	a := p.Spec.Affinity
	if a == nil || a.NodeAffinity == nil || a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return ""
	}
	for _, term := range a.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		for _, f := range term.MatchFields {
			if f.Key == "metadata.name" && f.Operator == corev1.NodeSelectorOpIn && len(f.Values) == 1 {
				return f.Values[0]
			}
		}
	}
	return ""
}

// sandboxEvidence is what a probe pod's events say about its sandbox.
type sandboxEvidence int

const (
	// The zero value: no sandbox event yet.
	_ sandboxEvidence = iota
	// sandboxCreated: an image pull or container start followed the sandbox,
	// so the pod network was set up.
	sandboxCreated
	// sandboxFailed: creating the sandbox failed, which only the pod network
	// or the container runtime explains.
	sandboxFailed
)

// sandboxEventReasons are the kubelet events that settle a pod's sandbox.
var sandboxEventReasons = map[string]sandboxEvidence{
	"Pulling":                sandboxCreated,
	"Pulled":                 sandboxCreated,
	"Created":                sandboxCreated,
	"Started":                sandboxCreated,
	"FailedCreatePodSandBox": sandboxFailed,
}

// probeSandboxEvents reads each probe pod's sandbox evidence from its events,
// the latest one deciding: a sandbox that failed and was then created on
// retry counts as created. It returns why the events could not be read, which
// leaves a pod with no IP undecided.
func probeSandboxEvents(
	ctx context.Context, client kubernetes.Interface, ns string,
) (map[string]sandboxEvidence, error) {
	events, err := observe(ctx, func(c context.Context) (*corev1.EventList, error) {
		return client.CoreV1().Events(ns).List(c, metav1.ListOptions{})
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(events.Items, func(i, j int) bool {
		return eventTime(&events.Items[i]).Before(eventTime(&events.Items[j]))
	})
	sandboxes := map[string]sandboxEvidence{}
	for i := range events.Items {
		e := &events.Items[i]
		if e.InvolvedObject.Kind != "Pod" {
			continue
		}
		if evidence, ok := sandboxEventReasons[e.Reason]; ok {
			sandboxes[e.InvolvedObject.Name] = evidence
		}
	}
	return sandboxes, nil
}

// checkerRefused reports whether the kubelet refused the checker pod at
// admission (OutOfpods, OutOfcpu): it ends Failed with a reason and no
// container ever ran.
func checkerRefused(pod *corev1.Pod) bool {
	if pod == nil || pod.Status.Phase != corev1.PodFailed || pod.Status.Reason == "" {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			return false
		}
	}
	return true
}

// eventTime is when an event last happened, whichever field the source set.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

// checkerFailureCause returns why a Failed checker pod ended when the cause
// was not a failed connection, or "" when the script reported one.
func checkerFailureCause(pod *corev1.Pod) string {
	if pod == nil {
		return "the checker's status was not returned"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		t := cs.State.Terminated
		if t == nil {
			continue
		}
		switch t.ExitCode {
		case nodeToNodeUnreachableExit:
			return ""
		case nodeToNodeNoNetcatExit:
			return "the probe image has no nc; set " + nodeToNodeImageEnv + " to a busybox-compatible image"
		default:
			return fmt.Sprintf("container exited %d (%s)", t.ExitCode, t.Reason)
		}
	}
	if pod.Status.Reason != "" {
		return fmt.Sprintf("rejected by the kubelet: %s", pod.Status.Reason)
	}
	return "the container never ran"
}

// probePodBlocker returns why a probe pod is stuck when the cause is the probe
// itself rather than the network, or "" when it is not. The kubelet rejects a
// pod at admission (Evicted under disk or PID pressure, OutOfcpu, OutOfpods)
// by failing it with a reason and no IP, which is no network fault.
func probePodBlocker(p *corev1.Pod) string {
	if p.Status.Phase == corev1.PodFailed {
		if p.Status.Reason != "" {
			return "rejected by the kubelet (" + p.Status.Reason + ")"
		}
		return "failed"
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return "Unschedulable"
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil &&
			(probeImagePullReasons[w.Reason] || probeContainerStartReasons[w.Reason]) {
			return w.Reason
		}
	}
	return ""
}

// nodeToNodeTolerations tolerates every taint, so the probe reaches every node
// it is pinned to, including a dedicated control plane and GPU nodes. Which
// nodes those are is decided by nodeToNodeTargets.
func nodeToNodeTolerations() []corev1.Toleration {
	return []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
}

func nodeToNodeSecurityContext() *corev1.SecurityContext {
	runAsNonRoot := true
	allowPrivEsc := false
	runAsUser := int64(65534)
	return &corev1.SecurityContext{
		RunAsNonRoot:             &runAsNonRoot,
		RunAsUser:                &runAsUser,
		AllowPrivilegeEscalation: &allowPrivEsc,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// buildNodeToNodeDaemonSet builds the probe server DaemonSet, pinned to nodes:
// it tolerates every taint, so without the pin it would also land on the
// nodes nodeToNodeTargets leaves out.
func buildNodeToNodeDaemonSet(
	name, namespace string, labels map[string]string, image string, nodes []string,
) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					// ActiveDeadlineSeconds is forbidden on DaemonSet pod templates.
					// Cleanup is handled by deleting the DaemonSet in the deferred sweep.
					RestartPolicy: corev1.RestartPolicyAlways,
					Tolerations:   nodeToNodeTolerations(),
					Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
						RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
							NodeSelectorTerms: []corev1.NodeSelectorTerm{{
								MatchFields: []corev1.NodeSelectorRequirement{{
									Key: metav1.ObjectNameField, Operator: corev1.NodeSelectorOpIn, Values: nodes,
								}},
							}},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "server",
						Image: image,
						Command: []string{"sh", "-c",
							fmt.Sprintf("while true; do nc -l -p %d; done", nodeToNodeTestPort)},
						Resources:       enforcementResources(),
						SecurityContext: nodeToNodeSecurityContext(),
					}},
				},
			},
		},
	}
}

func buildNodeToNodeCheckerPod(name, namespace, nodeName string, targetIPs []string, image string) *corev1.Pod {
	deadline := nodeToNodeActiveDeadline
	// A dedicated exit code for "could not connect" lets the caller tell a
	// network failure from every other way the pod can end Failed: a kubelet
	// admission rejection, an image without nc, a deadline.
	cmds := []string{fmt.Sprintf("command -v nc >/dev/null 2>&1 || exit %d", nodeToNodeNoNetcatExit)}
	for _, ip := range targetIPs {
		cmds = append(cmds,
			fmt.Sprintf("nc -z -w 5 %s %d || exit %d", ip, nodeToNodeTestPort, nodeToNodeUnreachableExit))
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
				"app.kubernetes.io/component":  "n2n-checker",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:              nodeName,
			RestartPolicy:         corev1.RestartPolicyNever,
			ActiveDeadlineSeconds: &deadline,
			// NodeName bypasses the scheduler but not the NodeRestriction /
			// taint admission plugin, so a control-plane node still rejects
			// this pod without the same tolerations the DaemonSet carries.
			Tolerations: nodeToNodeTolerations(),
			Containers: []corev1.Container{{
				Name:            "checker",
				Image:           image,
				Command:         []string{"sh", "-c", strings.Join(cmds, "; ")},
				Resources:       enforcementResources(),
				SecurityContext: nodeToNodeSecurityContext(),
			}},
		},
	}
}

// controlPlaneNamespaces lists the namespaces the self-managed stack deploys
// into, per deploy/stacks/self-managed/helmfile.d. Namespaces that are absent
// are skipped silently (a LIST against a missing namespace returns an empty
// 200), so listing one that a given install does not use is harmless.
//
// The OpenBao namespace is overridable via NVCF_OPENBAO_NAMESPACE, so its
// configured value replaces its default at runtime in controlPlaneNamespaceSet.
var controlPlaneNamespaces = []string{
	"nvcf", "sis", "api-keys", "ess", "nvcf-ui",
	"nats-system", "vault-system", "cassandra-system",
	"cert-manager", "envoy-gateway-system",
}

// openBaoNamespaceEnv mirrors the nvcf-cli override so a cluster that relocates
// OpenBao does not silently drop its StatefulSet from the Tier-2 check.
const openBaoNamespaceEnv = "NVCF_OPENBAO_NAMESPACE"

// controlPlaneNamespaceSet returns the namespaces Tier-1 and Tier-2 scan:
// controlPlaneNamespaces with a relocated OpenBao or Envoy Gateway namespace in
// place of its default, de-duplicated.
func controlPlaneNamespaceSet() []string {
	// Each override RELOCATES a component, so it replaces that component's
	// default namespace rather than adding to it. Appending left the defaults
	// in the set, so after relocating OpenBao to "openbao" the Tier-2 check
	// still assessed whatever foreign workload now occupies vault-system, and
	// a sealed third-party Vault there failed the whole run.
	relocations := []struct{ env, defaultNS string }{
		{openBaoNamespaceEnv, "vault-system"},
		{envoyGatewayNamespaceEnv, envoyGatewayNamespace},
	}
	replaced := make(map[string]string, len(relocations))
	for _, r := range relocations {
		if v := strings.TrimSpace(os.Getenv(r.env)); v != "" && v != r.defaultNS {
			replaced[r.defaultNS] = v
		}
	}

	out := make([]string, 0, len(controlPlaneNamespaces))
	seen := make(map[string]bool, len(controlPlaneNamespaces))
	for _, ns := range controlPlaneNamespaces {
		if to, ok := replaced[ns]; ok {
			ns = to
		}
		if !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	return out
}

// gatewaySet holds NVCF Gateways as "namespace/name" entries.
type gatewaySet map[string]bool

// nvcfGatewayNames parses NVCF_GATEWAY_NAMES into namespace/name entries.
// Empty means unconfigured. A bare name is returned in invalid rather than
// guessed at: it matched a same-named Gateway in any namespace, so another
// team's Gateway could be judged as NVCF's, and its own namespace was never
// searched for the proxy. The stack names every Gateway with a namespace.
func nvcfGatewayNames() (set gatewaySet, invalid []string) {
	set = gatewaySet{}
	for _, entry := range strings.Split(os.Getenv(nvcfGatewayNamesEnv), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ns, name, ok := strings.Cut(entry, "/"); !ok || ns == "" || name == "" || strings.Contains(name, "/") {
			invalid = append(invalid, entry)
			continue
		}
		set[entry] = true
	}
	return set, invalid
}

// entryFor returns the entry whose Gateway owns the object carrying labels (a
// proxy Service or Deployment), or "" when none does.
func (g gatewaySet) entryFor(labels map[string]string) string {
	name := labels[owningGatewayNameLabel]
	if name == "" {
		return ""
	}
	if qualified := labels[owningGatewayNamespaceLabel] + "/" + name; g[qualified] {
		return qualified
	}
	return ""
}

// entriesForProxy returns the entries a proxy Service or Deployment serves.
// A per-Gateway proxy names its Gateway. A merged-gateways proxy carries only
// its GatewayClass and serves every Gateway of that class, so it serves the
// NVCF entries whose Gateways use that class. The class match is limited to
// class-only proxies: a shared class name such as "eg" must not pull another
// team's per-Gateway proxy in.
func (g gatewaySet) entriesForProxy(labels map[string]string, classes map[string][]string) []string {
	if entry := g.entryFor(labels); entry != "" {
		return []string{entry}
	}
	if labels[owningGatewayNameLabel] == "" {
		return classes[labels[owningGatewayClassLabel]]
	}
	return nil
}

// nvcfGatewayClassesIn maps each GatewayClass used by an NVCF Gateway to the
// entries that use it, so merged-gateways proxies can be attributed.
func nvcfGatewayClassesIn(
	ctx context.Context, surface gatewayAPISurface, routes dynamic.Interface, g gatewaySet,
) (map[string][]string, error) {
	if len(g) == 0 {
		return nil, nil
	}
	version := surface.servedVersion("gateways")
	if version == "" {
		return nil, nil
	}
	if routes == nil {
		return nil, fmt.Errorf("no client available to list gateways")
	}
	gvr := schema.GroupVersionResource{Group: gatewayAPIGroup, Version: version, Resource: "gateways"}
	list, err := listDynamic(ctx, routes, gvr, metav1.ListOptions{})
	if err != nil {
		// The CRD can go between discovery and the List. No Gateways means no
		// classes, the same as the resource not being served.
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing gateways: %w", err)
	}
	classes := map[string][]string{}
	for i := range list.Items {
		gw := &list.Items[i]
		class, _, _ := unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
		entry := gw.GetNamespace() + "/" + gw.GetName()
		if !g[entry] {
			continue
		}
		if class != "" {
			classes[class] = append(classes[class], entry)
		}
	}
	return classes, nil
}

// namespaces returns the distinct Gateway namespaces in the set, excluding
// skip, so the caller can search them for proxy Services.
func (g gatewaySet) namespaces(skip string) []string {
	uniq := map[string]bool{}
	for entry := range g {
		if ns, _, ok := strings.Cut(entry, "/"); ok && ns != "" && ns != skip {
			uniq[ns] = true
		}
	}
	out := make([]string, 0, len(uniq))
	for ns := range uniq {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (g gatewaySet) sorted() []string {
	names := make([]string, 0, len(g))
	for n := range g {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// String renders the set in a stable order for messages.
func (g gatewaySet) String() string { return strings.Join(g.sorted(), ", ") }

const (
	// nvcfRoutesChartPrefix prefixes the helm.sh/chart label on every route the
	// nvcf-gateway-routes chart renders. That label is built from the chart
	// name, which nameOverride does not change, unlike app.kubernetes.io/name.
	nvcfRoutesChartPrefix = "nvcf-gateway-routes-"
	helmChartLabel        = "helm.sh/chart"
)

// gatewayRouteResources are the route kinds whose parentRefs attach NVCF
// traffic to a Gateway.
var gatewayRouteResources = []string{"httproutes", "grpcroutes", "tcproutes", "udproutes"}

// gatewayAPIVersionPreference picks one served version per route kind. The
// kinds do not share a version (TCPRoute and UDPRoute are v1alpha2 only), and
// any served version lists every object, so this only makes the choice stable.
var gatewayAPIVersionPreference = []string{"v1", "v1beta1", "v1alpha2"}

// servesRoutes reports whether the cluster serves any Gateway API route kind.
func (s gatewayAPISurface) servesRoutes() bool {
	for _, resource := range gatewayRouteResources {
		if s.servedVersion(resource) != "" {
			return true
		}
	}
	return false
}

func (s gatewayAPISurface) servedVersion(resource string) string {
	for _, v := range gatewayAPIVersionPreference {
		if s.hasPair(gatewayAPIGroup+"/"+v, resource) {
			return v
		}
	}
	return ""
}

// gatewayOwnership says which Envoy proxies are NVCF's. Run resolves it once
// and hands it to the LoadBalancer check and Tier-1: resolved separately,
// minutes apart, the two rows could judge different Gateways in one run.
type gatewayOwnership struct {
	// gateways are the NVCF Gateways as namespace/name, and source says where
	// they came from. An empty set with a nil err means no NVCF route exists.
	gateways gatewaySet
	source   string
	// err means ownership is unknown and must not be read as an empty set.
	err error
	// invalid holds configured entries that were ignored, and unlisted the
	// Gateways the NVCF routes attach to that a configured list leaves out.
	invalid  []string
	unlisted []string
	// routesMissing means a configured list is in use and no NVCF route
	// exists at all, though the cluster serves the route kinds.
	routesMissing bool
	// crossCheckErr is why a configured list could not be checked against
	// the NVCF routes, so unlisted Gateways and a missing routes release
	// would go unseen.
	crossCheckErr error

	surface         gatewayAPISurface
	routes          dynamic.Interface
	classesResolved bool
	classes         map[string][]string
	classErr        error
}

// resolveGatewayOwnershipIn resolves the NVCF Gateways over an already
// discovered surface. A configured list replaces discovery: the launcher knows
// the stack's Gateways, and a list set by hand is set because discovery does
// not describe the install.
func resolveGatewayOwnershipIn(
	ctx context.Context, surface gatewayAPISurface, surfaceErr error, routes dynamic.Interface,
) *gatewayOwnership {
	own := &gatewayOwnership{surface: surface, routes: routes}
	configured, invalid := nvcfGatewayNames()
	own.invalid = invalid
	if len(configured) > 0 {
		own.gateways, own.source = configured, nvcfGatewayNamesEnv
		// A configured list replaces discovery, so a Gateway it leaves out is
		// not assessed. Discover anyway to say so, and say when that failed.
		if surfaceErr != nil {
			own.crossCheckErr = fmt.Errorf("discovering Gateway API resources: %w", surfaceErr)
			return own
		}
		discovered, err := discoverNVCFGatewaysIn(ctx, surface, routes)
		if err != nil {
			own.crossCheckErr = err
			return own
		}
		for _, e := range discovered.sorted() {
			if !configured[e] {
				own.unlisted = append(own.unlisted, e)
			}
		}
		// The routes release may be missing. Discovery says so after install,
		// and a configured list must not hide it.
		own.routesMissing = len(discovered) == 0 && surface.servesRoutes()
		return own
	}
	if surfaceErr != nil {
		own.gateways, own.err = gatewaySet{}, fmt.Errorf("discovering Gateway API resources: %w", surfaceErr)
		return own
	}
	discovered, err := discoverNVCFGatewaysIn(ctx, surface, routes)
	if err != nil {
		own.gateways, own.err = gatewaySet{}, err
		return own
	}
	own.gateways, own.source = discovered, "from NVCF routes"
	return own
}

// reportInvalid warns once per run about ignored gateway-name entries, about
// NVCF Gateways a configured list leaves out, and, after install, about a
// configured list with no NVCF route at all.
func (o *gatewayOwnership) reportInvalid(log *logrus.Entry, state *ValidationState) {
	if len(o.invalid) > 0 {
		msg := fmt.Sprintf("ignoring %s entries without a namespace: %s; name each Gateway as namespace/name",
			nvcfGatewayNamesEnv, strings.Join(o.invalid, ", "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Gateway names: "+msg)
		o.invalid = nil
	}
	if len(o.unlisted) > 0 {
		msg := fmt.Sprintf("NVCF routes also attach to %s, which %s leaves out, so the checks do not assess "+
			"them; list every NVCF Gateway", strings.Join(o.unlisted, ", "), nvcfGatewayNamesEnv)
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Gateway names: "+msg)
		o.unlisted = nil
	}
	if o.crossCheckErr != nil {
		msg := fmt.Sprintf("could not read the NVCF routes to confirm the %s list: %v; %s",
			nvcfGatewayNamesEnv, o.crossCheckErr, readAdvice(o.crossCheckErr))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Gateway names: "+msg)
		o.crossCheckErr = nil
	}
	if o.routesMissing && state.PostInstall {
		msg := fmt.Sprintf("no NVCF routes found although the control plane is installed, so nothing reaches "+
			"the Gateways %s names (%s); check that the gateway routes release is installed",
			nvcfGatewayNamesEnv, o.gateways)
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Gateway routes: "+msg)
	}
	o.routesMissing = false
}

// gatewayClasses lists the NVCF Gateways' classes on first use. Only a
// merged-gateways proxy needs them, so an install without one never pays for
// the list.
func (o *gatewayOwnership) gatewayClasses(ctx context.Context) (map[string][]string, error) {
	if !o.classesResolved {
		o.classes, o.classErr = nvcfGatewayClassesIn(ctx, o.surface, o.routes, o.gateways)
		o.classesResolved = true
	}
	return o.classes, o.classErr
}

// proxyOwner returns the NVCF entries a proxy with these labels serves, and
// whether that could be decided. known with no entries means another team's.
func (o *gatewayOwnership) proxyOwner(ctx context.Context, labels map[string]string) (entries []string, known bool) {
	if o.err != nil {
		return nil, false
	}
	if entry := o.gateways.entryFor(labels); entry != "" {
		return []string{entry}, true
	}
	if labels[owningGatewayNameLabel] != "" || len(o.gateways) == 0 {
		return nil, true
	}
	classes, err := o.gatewayClasses(ctx)
	if err != nil {
		return nil, false
	}
	return classes[labels[owningGatewayClassLabel]], true
}

// discoverNVCFGatewaysIn collects the Gateways that routes rendered by the
// nvcf-gateway-routes chart attach to.
// Route kinds the cluster does not serve are skipped; an error listing a
// served kind is returned, not treated as absence.
func discoverNVCFGatewaysIn(
	ctx context.Context, surface gatewayAPISurface, routes dynamic.Interface,
) (gatewaySet, error) {
	set := gatewaySet{}
	for _, resource := range gatewayRouteResources {
		version := surface.servedVersion(resource)
		if version == "" {
			continue
		}
		if routes == nil {
			return nil, fmt.Errorf("no client available to list %s", resource)
		}
		gvr := schema.GroupVersionResource{Group: gatewayAPIGroup, Version: version, Resource: resource}
		// Filter server-side on the label's presence; its value is a
		// chart-plus-version string, so the prefix is matched client-side.
		list, err := listDynamic(ctx, routes, gvr, metav1.ListOptions{LabelSelector: helmChartLabel})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// The CRD went away between discovery and the list.
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", resource, err)
		}
		for i := range list.Items {
			route := &list.Items[i]
			if !strings.HasPrefix(route.GetLabels()[helmChartLabel], nvcfRoutesChartPrefix) {
				continue
			}
			for _, ref := range gatewayParentRefs(route) {
				set[ref] = true
			}
		}
	}
	return set, nil
}

// listDynamic lists gvr cluster-wide, retrying transient errors.
func listDynamic(
	ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource, opts metav1.ListOptions,
) (*unstructured.UnstructuredList, error) {
	return observe(ctx, func(c context.Context) (*unstructured.UnstructuredList, error) {
		return client.Resource(gvr).List(c, opts)
	})
}

// gatewayParentRefs returns a route's Gateway parents as "namespace/name",
// applying the Gateway API defaults: group gateway.networking.k8s.io, kind
// Gateway, and the route's own namespace. Parents of other kinds are skipped.
// The CRD schema requires a non-empty name, so an empty one is only skipped
// defensively.
func gatewayParentRefs(route *unstructured.Unstructured) []string {
	refs, _, _ := unstructured.NestedSlice(route.Object, "spec", "parentRefs")
	var out []string
	for _, raw := range refs {
		ref, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if group, set := ref["group"]; set && group != gatewayAPIGroup {
			continue
		}
		if kind, set := ref["kind"]; set && kind != "Gateway" {
			continue
		}
		name, _ := ref["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := ref["namespace"].(string)
		if ns == "" {
			ns = route.GetNamespace()
		}
		out = append(out, ns+"/"+name)
	}
	return out
}

// envoyGatewayNamespaceName is where the Envoy Gateway controller and, by
// default, its provisioned proxies live.
func envoyGatewayNamespaceName() string {
	if ns := strings.TrimSpace(os.Getenv(envoyGatewayNamespaceEnv)); ns != "" {
		return ns
	}
	return envoyGatewayNamespace
}

// isEnvoyProxy reports whether a Deployment is an Envoy Gateway data-plane
// proxy rather than the controller. Merged-gateways mode labels its single
// proxy by GatewayClass only.
func isEnvoyProxy(labels map[string]string) bool {
	return labels[owningGatewayNameLabel] != "" || labels[owningGatewayClassLabel] != ""
}

// rolloutReadyFloor is the fewest Ready pods a healthy rollout of d can leave.
// For RollingUpdate that is spec.replicas minus maxUnavailable, which the
// Deployment controller never goes below; it resolves the fenceposts the same
// way (maxUnavailable rounds down, maxSurge up, and both zero means one).
// Recreate replaces every pod at once, so it has no floor.
func rolloutReadyFloor(d *appsv1.Deployment, want int32) int32 {
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return 0
	}
	maxUnavailable, maxSurge := intstr.FromString("25%"), intstr.FromString("25%")
	if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
		if ru.MaxUnavailable != nil {
			maxUnavailable = *ru.MaxUnavailable
		}
		if ru.MaxSurge != nil {
			maxSurge = *ru.MaxSurge
		}
	}
	unavailable, err := intstr.GetScaledValueFromIntOrPercent(&maxUnavailable, int(want), false)
	if err != nil {
		// Unparsable: no floor rather than a failure the operator cannot act on.
		return 0
	}
	surge, err := intstr.GetScaledValueFromIntOrPercent(&maxSurge, int(want), true)
	if err == nil && unavailable == 0 && surge == 0 {
		unavailable = 1
	}
	if unavailable >= int(want) {
		// Everything may be unavailable at once, so there is no floor. This
		// also bounds the conversion below.
		return 0
	}
	return want - int32(unavailable) // #nosec G115 -- 0 <= unavailable < want, an int32
}

// rolloutProgressingReasons are the Progressing condition reasons the
// Deployment controller sets while a rollout is moving. Once it finishes the
// reason is NewReplicaSetAvailable, and it stays that way even when a pod of
// the finished ReplicaSet can never be created again.
var rolloutProgressingReasons = map[string]bool{
	"ReplicaSetUpdated":    true,
	"NewReplicaSetCreated": true,
	"FoundNewReplicaSet":   true,
}

// deploymentRollingOut reports whether d is mid-rollout, which is what earns
// the below-target tolerance. UpdatedReplicas < want alone also describes a
// ReplicaSet whose pods are refused by a quota or webhook, and that never
// reports ProgressDeadlineExceeded once the rollout has completed. So the
// controller has to say it is still rolling: either it has not observed the
// new generation yet, or its Progressing reason is a rollout step. A
// ReplicaFailure is never a rollout. A paused Deployment is held, not rolling.
//
// A Deployment with no progress deadline (progressDeadlineSeconds at its
// MaxInt32 sentinel) has no Progressing condition at all, since the
// controller keeps it only to report the deadline. There, pods of an older
// ReplicaSet still running beside too few updated ones are the rollout.
func deploymentRollingOut(d *appsv1.Deployment, want int32) bool {
	if d.Spec.Paused {
		return false
	}
	progressing := false
	for i := range d.Status.Conditions {
		c := &d.Status.Conditions[i]
		switch c.Type {
		case appsv1.DeploymentReplicaFailure:
			if c.Status == corev1.ConditionTrue {
				return false
			}
		case appsv1.DeploymentProgressing:
			progressing = c.Status == corev1.ConditionTrue && rolloutProgressingReasons[c.Reason]
		}
	}
	if d.Status.ObservedGeneration < d.Generation {
		return true
	}
	if !hasProgressDeadline(d) {
		return d.Status.UpdatedReplicas < want && d.Status.Replicas > d.Status.UpdatedReplicas
	}
	return d.Status.UpdatedReplicas < want && progressing
}

// hasProgressDeadline reports whether the Deployment controller tracks a
// progress deadline for d. MaxInt32 is the API's "no deadline" sentinel.
func hasProgressDeadline(d *appsv1.Deployment) bool {
	return d.Spec.ProgressDeadlineSeconds == nil || *d.Spec.ProgressDeadlineSeconds != math.MaxInt32
}

// deploymentRolloutStalled reports whether the Deployment controller has given
// up on the current rollout. Kubernetes sets Progressing=False with reason
// ProgressDeadlineExceeded once progressDeadlineSeconds elapses without
// progress, which is what distinguishes a wedged rollout (bad image, no
// schedulable node) from one that is merely in flight.
func deploymentRolloutStalled(d *appsv1.Deployment) bool {
	for i := range d.Status.Conditions {
		c := &d.Status.Conditions[i]
		if c.Type == appsv1.DeploymentProgressing &&
			c.Status == corev1.ConditionFalse &&
			c.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

// sharedTier1Namespaces hold third-party controllers NVCF installs beside, or
// may find already installed. A Deployment there that is scaled to zero may be
// someone else's parked controller, so it warns rather than failing the row.
func sharedTier1Namespaces() map[string]bool {
	return map[string]bool{"cert-manager": true, envoyGatewayNamespaceName(): true}
}

// checkTier1DeploymentsFor verifies that every Deployment in the control-plane
// namespaces has readyReplicas >= spec.replicas. Any under-replicated Deployment
// means HA headroom is gone and a second failure causes a full outage.
//
// The check is generic; no hardcoded Deployment names. New services added to
// those namespaces are automatically covered.
//
// Critical: under-replication means a single additional failure causes a full
// service outage.
func checkTier1DeploymentsFor(
	ctx context.Context, client kubernetes.Interface, own *gatewayOwnership, state *ValidationState,
) {
	printHeader(state.Log, "Tier-1 Deployment Readiness")
	own.reportInvalid(state.Log, state)
	scan := newTier1Scan(own, state)
	for _, ns := range scan.namespaces {
		scan.scanNamespace(ctx, client, ns)
	}
	proxiesUnobserved := scan.reportProxyAttribution()
	if scan.checkGatewayCoverage(ctx, client) {
		proxiesUnobserved = true
	}
	scan.verdict(proxiesUnobserved)
}

// tier1Scan collects what Tier-1 saw across the scanned namespaces.
type tier1Scan struct {
	log   *logrus.Entry
	state *ValidationState
	own   *gatewayOwnership

	// Envoy Gateway runs a proxy Deployment per Gateway, or one per class in
	// merged-gateways mode, beside its controller, including for other
	// teams' Gateways, so only NVCF's proxies are assessed. GatewayNamespace
	// mode puts each proxy beside its Gateway, so the Gateway namespaces hold
	// proxies too (gatewayNS); those not otherwise scanned are searched for
	// proxies only (proxyOnly).
	envoyNS    string
	namespaces []string
	gatewayNS  map[string]bool
	proxyOnly  map[string]bool
	shared     map[string]bool
	// noGatewaysPostInstall: installed with no NVCF Gateway named anywhere,
	// every proxy is treated as possibly NVCF's.
	noGatewaysPostInstall bool

	underReplicated []string
	scaledToZero    []string
	gatewayGaps     []string
	checkedCount    int
	// unread holds each namespace whose Deployments could not be read, with
	// the cause, and unreadErrs the errors themselves.
	unread       []string
	unreadErrs   []error
	rollingCount int
	// rollingUnderReplicated counts tolerated rollouts that are below their
	// replica target but still at or above their rollout floor.
	rollingUnderReplicated int

	// skippedProxies are other teams' proxies. unattributedDown are proxies
	// whose owner could not be decided and that are not Ready: a Ready proxy
	// says nothing bad about the tier whoever owns it, so only these leave
	// the row undecided.
	skippedProxies   int
	unattributed     int
	unattributedDown []string
}

func newTier1Scan(own *gatewayOwnership, state *ValidationState) *tier1Scan {
	scan := &tier1Scan{
		log: state.Log, state: state, own: own,
		envoyNS:               envoyGatewayNamespaceName(),
		namespaces:            controlPlaneNamespaceSet(),
		gatewayNS:             map[string]bool{},
		proxyOnly:             map[string]bool{},
		shared:                sharedTier1Namespaces(),
		noGatewaysPostInstall: own.err == nil && len(own.gateways) == 0 && state.PostInstall,
	}
	for _, ns := range own.gateways.namespaces(scan.envoyNS) {
		scan.gatewayNS[ns] = true
		if !slices.Contains(scan.namespaces, ns) {
			scan.namespaces = append(scan.namespaces, ns)
			scan.proxyOnly[ns] = true
		}
	}
	return scan
}

func (s *tier1Scan) warn(msg string) {
	printWarning(s.log, msg)
	s.state.Warnings = append(s.state.Warnings, "Tier-1 Deployments: "+msg)
}

func (s *tier1Scan) scanNamespace(ctx context.Context, client kubernetes.Interface, ns string) {
	deploys, err := observe(ctx, func(c context.Context) (*appsv1.DeploymentList, error) {
		return client.AppsV1().Deployments(ns).List(c, metav1.ListOptions{})
	})
	if err != nil {
		// An unread namespace was not observed, not found healthy. Track it
		// so it cannot reach the trivial-pass exit below, and keep going:
		// returning here threw away the under-replicated Deployments already
		// collected from earlier namespaces. A LIST against a missing
		// namespace returns an empty 200, so IsNotFound is not a case here.
		printWarning(s.log, readFailure("Deployments in "+ns, err))
		s.unread = append(s.unread, fmt.Sprintf("%s (%v)", ns, err))
		s.unreadErrs = append(s.unreadErrs, err)
		return
	}
	for i := range deploys.Items {
		s.assess(ctx, ns, &deploys.Items[i])
	}
}

func (s *tier1Scan) assess(ctx context.Context, ns string, d *appsv1.Deployment) {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	proxy := isEnvoyProxy(d.Labels) && (ns == s.envoyNS || s.gatewayNS[ns])
	if s.proxyOnly[ns] && !proxy {
		return
	}
	if proxy && s.settleProxy(ctx, ns, d, want) {
		return
	}
	// A Deployment scaled to zero satisfies "ReadyReplicas >= want" with
	// nothing running at all, so counting it as healthy lets a maintenance
	// scale-down or a replicaCount:0 values error publish the critical row as
	// All Ready. It fails rather than warns: the same Deployment at 2/3
	// already fails, and "fully down" must not score better than
	// "degraded". In a shared namespace it may be another install's parked
	// controller, such as an external cert-manager, so there it only warns.
	if want == 0 {
		if s.shared[ns] && !proxy {
			s.warn(fmt.Sprintf("%s/%s is scaled to zero replicas", ns, d.Name))
			return
		}
		s.scaledToZero = append(s.scaledToZero, ns+"/"+d.Name)
		return
	}
	// A rollout transiently drops readyReplicas below spec.replicas on a
	// healthy cluster, so tolerate one that is moving (deploymentRollingOut),
	// has not stalled (ProgressDeadlineExceeded), and is at or above the
	// readiness floor no healthy RollingUpdate drops below.
	if deploymentRollingOut(d, want) && !deploymentRolloutStalled(d) &&
		d.Status.ReadyReplicas >= rolloutReadyFloor(d, want) {
		s.warn(fmt.Sprintf("%s/%s: %s (updated: %d/%d); re-run check after rollout completes",
			ns, d.Name, RolloutInProgressMarker, d.Status.UpdatedReplicas, want))
		s.rollingCount++
		if d.Status.ReadyReplicas < want {
			s.rollingUnderReplicated++
		}
		return
	}
	s.checkedCount++
	if d.Status.ReadyReplicas < want {
		s.underReplicated = append(s.underReplicated,
			fmt.Sprintf("%s/%s (ready: %d, want: %d)", ns, d.Name, d.Status.ReadyReplicas, want))
	}
}

// settleProxy handles an Envoy proxy that is not an NVCF proxy to assess like
// any other Deployment, and reports whether it did.
func (s *tier1Scan) settleProxy(ctx context.Context, ns string, d *appsv1.Deployment, want int32) bool {
	entries, known := s.own.proxyOwner(ctx, d.Labels)
	if !known || s.noGatewaysPostInstall {
		s.unattributed++
		if want == 0 || d.Status.ReadyReplicas < want {
			s.unattributedDown = append(s.unattributedDown, fmt.Sprintf("%s/%s (ready: %d, want: %d)",
				ns, d.Name, d.Status.ReadyReplicas, want))
		} else {
			// Assessed and Ready, whoever owns it.
			s.checkedCount++
		}
		return true
	}
	if len(entries) == 0 {
		s.skippedProxies++
		return true
	}
	return false
}

// reportProxyAttribution warns about proxies whose owner could not be decided
// and reports whether any of them leaves the row undecided. A proxy whose owner
// is unknown was still assessed for readiness: only one that is not Ready
// could be NVCF's own outage, and an observed failure still decides it first.
func (s *tier1Scan) reportProxyAttribution() bool {
	proxiesUnobserved := len(s.unattributedDown) > 0
	switch why := s.own.err; {
	case why == nil && s.noGatewaysPostInstall:
		why = errNoNVCFGatewaysPostInstall
		fallthrough
	case why != nil:
		if proxiesUnobserved {
			s.warn(fmt.Sprintf("could not tell which Envoy proxies are NVCF's (%v), and %d are not Ready: %s",
				why, len(s.unattributedDown), strings.Join(s.unattributedDown, ", ")))
		} else if s.unattributed > 0 {
			printInfo(s.log, fmt.Sprintf("  Envoy proxy ownership unknown (%v), but all %d proxies are Ready",
				why, s.unattributed))
		}
	case proxiesUnobserved:
		s.warn(fmt.Sprintf("could not read the NVCF Gateways' classes to attribute merged-gateways proxies, "+
			"and %d are not Ready: %s", len(s.unattributedDown), strings.Join(s.unattributedDown, ", ")))
	case s.skippedProxies > 0 && len(s.own.gateways) == 0:
		s.warn(fmt.Sprintf("%d Envoy proxy Deployment(s) in %s not assessed: no NVCF routes found to identify "+
			"the NVCF Gateways; set clusterValidator.gatewayNames (env %s) if they exist", s.skippedProxies, s.envoyNS,
			nvcfGatewayNamesEnv))
	}
	return proxiesUnobserved
}

// checkGatewayCoverage fails an installed control plane with an NVCF Gateway
// that does not exist, or that Envoy Gateway runs and that has no Ready
// proxy: route discovery skipping a proxy-less Gateway is exactly the outage
// the post-install signal is for. A Gateway another implementation runs is
// not judged, since its data plane is not an Envoy proxy. It reports whether
// coverage could not be decided, which leaves the row unknown: anything the
// check could not look at is undecided, never a pass.
func (s *tier1Scan) checkGatewayCoverage(ctx context.Context, client kubernetes.Interface) bool {
	if !s.state.PostInstall {
		return false
	}
	if s.own.err != nil {
		// The NVCF Gateways are unknown, so a missing proxy would go unseen.
		s.warn(fmt.Sprintf("could not confirm every NVCF Gateway has a proxy: %v", s.own.err))
		return true
	}
	if len(s.own.gateways) == 0 {
		// No NVCF Gateway is named: reportProxyAttribution says so.
		return false
	}
	classOf, err := nvcfGatewayClassOf(ctx, s.own.surface, s.own.routes, s.own.gateways)
	if err != nil {
		s.warn(fmt.Sprintf("could not confirm the NVCF Gateways exist and have a proxy: %v; %s",
			err, readAdvice(err)))
		return true
	}
	// Record the Gateways seen not to exist first: that finding stands
	// whatever a later read says.
	var existing []string
	for _, e := range s.own.gateways.sorted() {
		if _, exists := classOf[e]; exists {
			existing = append(existing, e)
		} else {
			s.gatewayGaps = append(s.gatewayGaps, "NVCF Gateway "+e+" does not exist")
		}
	}
	undecided := len(existing) > 0 && s.envoyCoverageUndecided(ctx, client, existing, classOf)
	if len(s.gatewayGaps) > 0 {
		s.checkedCount++
	}
	return undecided
}

// envoyCoverageUndecided adds a gap for each of existing, the NVCF Gateways
// that exist, that Envoy Gateway runs and that has no Ready proxy, and
// reports whether that could not be decided.
func (s *tier1Scan) envoyCoverageUndecided(
	ctx context.Context, client kubernetes.Interface, existing []string, classOf map[string]string,
) bool {
	envoyClasses, err := envoyGatewayClasses(ctx, s.own.surface, s.own.routes)
	if err != nil {
		s.warn(fmt.Sprintf("could not tell which NVCF Gateways Envoy Gateway runs, so the proxies of %s were "+
			"not confirmed: %v; %s", strings.Join(existing, ", "), err, readAdvice(err)))
		return true
	}
	var envoyEntries []string
	envoyByClass := map[string][]string{}
	for _, e := range existing {
		if class := classOf[e]; envoyClasses[class] {
			envoyEntries = append(envoyEntries, e)
			envoyByClass[class] = append(envoyByClass[class], e)
		}
	}
	if len(envoyEntries) == 0 {
		return false
	}
	gaps, err := s.envoyProxyGaps(ctx, client, envoyEntries, envoyByClass)
	s.gatewayGaps = append(s.gatewayGaps, gaps...)
	if err != nil {
		s.warn(fmt.Sprintf("could not confirm every NVCF Gateway has a proxy: %v; %s", err, readAdvice(err)))
		return true
	}
	return false
}

// envoyProxyGaps returns a finding for each of entries, the NVCF Gateways
// Envoy Gateway runs, that has no proxy with a Ready pod. byClass maps each of
// their classes to its entries, for merged-gateways proxies.
func (s *tier1Scan) envoyProxyGaps(
	ctx context.Context, client kubernetes.Interface, entries []string, byClass map[string][]string,
) ([]string, error) {
	proxies, err := listEnvoyProxies(ctx, client)
	if err != nil {
		return nil, err
	}
	ready, found := map[string]bool{}, map[string][]string{}
	for _, p := range proxies {
		for _, e := range s.own.gateways.entriesForProxy(p.labels, byClass) {
			found[e] = append(found[e], p.ref)
			if p.ready {
				ready[e] = true
			}
		}
	}
	var gaps []string
	for _, e := range entries {
		switch {
		case ready[e]:
		case len(found[e]) > 0:
			gaps = append(gaps, fmt.Sprintf("NVCF Gateway %s has no Ready Envoy proxy (%s)",
				e, strings.Join(found[e], ", ")))
		default:
			gaps = append(gaps, "NVCF Gateway "+e+" has no Envoy proxy")
		}
	}
	return gaps, nil
}

// envoyProxy is an Envoy Gateway data-plane workload, and whether any of its
// pods is Ready.
type envoyProxy struct {
	ref    string
	labels map[string]string
	ready  bool
}

// listEnvoyProxies returns every Envoy proxy Deployment and DaemonSet in the
// cluster. Searching every namespace finds them wherever Envoy Gateway runs,
// including a controller namespace the validator was not told about, and
// GatewayNamespace mode's proxies beside their Gateways.
func listEnvoyProxies(ctx context.Context, client kubernetes.Interface) ([]envoyProxy, error) {
	seen := map[string]bool{}
	var out []envoyProxy
	add := func(kind, ns, name string, labels map[string]string, ready bool) {
		ref := kind + " " + ns + "/" + name
		if !seen[ref] && isEnvoyProxy(labels) {
			seen[ref] = true
			out = append(out, envoyProxy{ref: ref, labels: labels, ready: ready})
		}
	}
	for _, label := range []string{owningGatewayNameLabel, owningGatewayClassLabel} {
		opts := metav1.ListOptions{LabelSelector: label}
		deployments, err := observe(ctx, func(c context.Context) (*appsv1.DeploymentList, error) {
			return client.AppsV1().Deployments(metav1.NamespaceAll).List(c, opts)
		})
		if err != nil {
			return nil, fmt.Errorf("listing Envoy proxy Deployments: %w", err)
		}
		for i := range deployments.Items {
			d := &deployments.Items[i]
			add("Deployment", d.Namespace, d.Name, d.Labels, d.Status.ReadyReplicas > 0)
		}
		daemonSets, err := observe(ctx, func(c context.Context) (*appsv1.DaemonSetList, error) {
			return client.AppsV1().DaemonSets(metav1.NamespaceAll).List(c, opts)
		})
		if err != nil {
			return nil, fmt.Errorf("listing Envoy proxy DaemonSets: %w", err)
		}
		for i := range daemonSets.Items {
			ds := &daemonSets.Items[i]
			add("DaemonSet", ds.Namespace, ds.Name, ds.Labels, ds.Status.NumberReady > 0)
		}
	}
	return out, nil
}

// nvcfGatewayClassOf returns the class of each NVCF Gateway that exists. A
// Gateway missing from the result was observed not to exist.
func nvcfGatewayClassOf(
	ctx context.Context, surface gatewayAPISurface, routes dynamic.Interface, g gatewaySet,
) (map[string]string, error) {
	version := surface.servedVersion("gateways")
	if version == "" {
		return nil, errors.New("the cluster does not serve the Gateway API gateways")
	}
	if routes == nil {
		return nil, errors.New("no client available to list gateways")
	}
	gateways, err := listDynamic(ctx, routes,
		schema.GroupVersionResource{Group: gatewayAPIGroup, Version: version, Resource: "gateways"},
		metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing gateways: %w", err)
	}
	classOf := map[string]string{}
	for i := range gateways.Items {
		gw := &gateways.Items[i]
		if entry := gw.GetNamespace() + "/" + gw.GetName(); g[entry] {
			classOf[entry], _, _ = unstructured.NestedString(gw.Object, "spec", "gatewayClassName")
		}
	}
	return classOf, nil
}

// envoyGatewayClasses returns the GatewayClasses Envoy Gateway runs.
func envoyGatewayClasses(
	ctx context.Context, surface gatewayAPISurface, routes dynamic.Interface,
) (map[string]bool, error) {
	version := surface.servedVersion("gatewayclasses")
	if version == "" {
		return nil, errors.New("the cluster does not serve the Gateway API gatewayclasses")
	}
	if routes == nil {
		return nil, errors.New("no client available to list gatewayclasses")
	}
	classes, err := listDynamic(ctx, routes,
		schema.GroupVersionResource{Group: gatewayAPIGroup, Version: version, Resource: "gatewayclasses"},
		metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing gatewayclasses: %w", err)
	}
	envoyClasses := map[string]bool{}
	for i := range classes.Items {
		c := &classes.Items[i]
		if controller, _, _ := unstructured.NestedString(c.Object, "spec", "controllerName"); controller ==
			envoyGatewayControllerName {
			envoyClasses[c.GetName()] = true
		}
	}
	return envoyClasses, nil
}

func (s *tier1Scan) setOK(ok bool) {
	s.state.Tier1DeploymentsOK = &ok
}

const tier1ProxiesUnknown = "Tier-1 Deployments: status unknown (NVCF Envoy proxies could not be identified, " +
	"so they were not assessed)"

func (s *tier1Scan) deniedWarning() string {
	return fmt.Sprintf("Tier-1 Deployments: could not read Deployments in %d control-plane namespace(s): %s",
		len(s.unread), readFailures(s.unread, s.unreadErrs))
}

// verdict decides the row. An observed failure wins over anything unobserved,
// and a tolerated rollout is a pass with a warning, not UNKNOWN.
func (s *tier1Scan) verdict(proxiesUnobserved bool) {
	if len(s.unread) > 0 {
		// Reported whatever decides the row, so a namespace that was never
		// read is not first heard of once the failure beside it is fixed.
		s.state.Warnings = append(s.state.Warnings, s.deniedWarning())
	}
	if s.checkedCount == 0 {
		s.verdictNothingAssessed(proxiesUnobserved)
		return
	}
	failures := s.underReplicated
	if len(s.scaledToZero) > 0 {
		failures = append(failures, fmt.Sprintf("scaled to zero replicas: %s", strings.Join(s.scaledToZero, ", ")))
	}
	failures = append(failures, s.gatewayGaps...)
	switch {
	case len(failures) > 0:
		printError(s.log, fmt.Sprintf("Under-replicated Deployments (%d):", len(failures)))
		for _, name := range failures {
			printInfo(s.log, "  "+name)
		}
		s.state.Recommendations = append(s.state.Recommendations,
			"Check for crashed, evicted, or unschedulable pods in the listed namespaces. "+
				"If a service is intentionally single-replica, raise its replicaCount in the "+
				"self-managed stack values to keep HA headroom.")
		s.setOK(false)
	case proxiesUnobserved:
		s.state.Warnings = append(s.state.Warnings, tier1ProxiesUnknown)
	case len(s.unread) > 0:
		// Some namespaces were never observed, so "all ready" is not a claim we
		// can make even though every Deployment we could see passed.
		printWarning(s.log, fmt.Sprintf("%d Deployment(s) ready, but %d namespace(s) were not readable",
			s.checkedCount, len(s.unread)))
	case s.rollingUnderReplicated > 0:
		// Tolerated, not unknown. Rolling one pod at a time is what an upgrade
		// looks like; reporting it as an unobserved critical check made every
		// control-plane upgrade NVCF-Not-Ready with a non-zero exit.
		s.warn(fmt.Sprintf("%d Deployment(s) ready, %d %s and below their replica target",
			s.checkedCount, s.rollingUnderReplicated, MidRolloutMarker))
		s.setOK(true)
	default:
		if s.rollingCount > 0 {
			printWarning(s.log, fmt.Sprintf("%d Deployment(s) ready, %d %s but still at their replica target",
				s.checkedCount, s.rollingCount, MidRolloutMarker))
		}
		printSuccess(s.log, fmt.Sprintf("All %d assessed Deployment(s) in control-plane namespaces are fully ready",
			s.checkedCount))
		s.setOK(true)
	}
}

func (s *tier1Scan) verdictNothingAssessed(proxiesUnobserved bool) {
	switch {
	case len(s.scaledToZero) > 0:
		// An observed failure wins over unreadable namespaces and tolerated
		// rollouts, as it does when something was assessed.
		printError(s.log, fmt.Sprintf("%d Deployment(s) are scaled to zero replicas: %s",
			len(s.scaledToZero), strings.Join(s.scaledToZero, ", ")))
		s.setOK(false)
	case proxiesUnobserved:
		s.state.Warnings = append(s.state.Warnings, tier1ProxiesUnknown)
	case len(s.unread) > 0:
		// Leave nil: nothing was assessed and at least one namespace could not
		// be read, so an empty result is not evidence of pre-install.
		printWarning(s.log, fmt.Sprintf("Deployments not readable in %d control-plane namespace(s)", len(s.unread)))
	case s.rollingCount > 0:
		// A tolerated rollout is a pass with a warning. Stalled rollouts and
		// those below their readiness floor never get here: they were assessed
		// and reach the failure path instead.
		s.warn(fmt.Sprintf("all %d Deployment(s) are %s; re-run after the rollout completes",
			s.rollingCount, MidRolloutMarker))
		s.setOK(true)
	case !s.state.PostInstall:
		printInfo(s.log, "  No Deployments found in control-plane namespaces (pre-install state)")
		s.setOK(true)
	default:
		// The launcher says the control plane is installed, so finding nothing
		// means the namespace list does not match the install (a relocated
		// release, an override left unset) or the services are gone.
		printError(s.log, "No Deployments found in any control-plane namespace")
		s.state.Recommendations = append(s.state.Recommendations,
			"Tier-1 found no Deployments. Check that the control plane is installed in this cluster, and set "+
				"clusterValidator.openBaoNamespace / envoyGatewayNamespace if those components were relocated.")
		s.setOK(false)
	}
}

// checkTier2StatefulSets verifies quorum membership and node placement for
// Tier-2 stateful components (NATS JetStream, OpenBao Raft, Cassandra).
// Any StatefulSet with an odd spec.replicas of 3 or more is treated as a quorum
// component and checked for:
//  1. readyReplicas == spec.replicas
//  2. all pods on distinct nodes, when the StatefulSet spreads its pods
//
// Whether a component is meant to be highly available is read from what was
// rendered rather than passed in: the stack gives its quorum StatefulSets pod
// anti-affinity under highAvailability.mode preferred or enforced, and none
// under mode none. A spread component below three replicas has lost its
// quorum. One that is not spread is checked for readiness only, since one
// replica and co-located pods are what mode none deploys.
//
// A StatefulSet mid-RollingUpdate with one pod down passes with a warning
// while the rollout can finish: it rolls one pod at a time, so that is the
// steady state for the duration of any upgrade. More than one pod down, or a
// rollout that stops making progress, fails.
//
// Any odd-sized StatefulSet of three or more in the control-plane namespaces
// is assessed. The stack's own quorum components (knownQuorumComponents) are
// also judged below three replicas and at even sizes.
//
// Critical: broken quorum or co-located peers leave the stack one failure
// away from a total control-plane outage.
func checkTier2StatefulSets(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	printHeader(state.Log, "Tier-2 StatefulSet Quorum and Placement")
	scan := &tier2Scan{log: state.Log, state: state}
	for _, ns := range controlPlaneNamespaceSet() {
		scan.scanNamespace(ctx, client, ns)
	}
	scan.verdict()
}

// minQuorumSize is the smallest replica count that holds a quorum through the
// loss of one member.
const minQuorumSize = int32(3)

// tier2Scan collects what Tier-2 saw across the control-plane namespaces.
type tier2Scan struct {
	log   *logrus.Entry
	state *ValidationState

	failures      []string
	skippedParity []string
	// unspread holds assessed StatefulSets with no pod anti-affinity, whose
	// placement is not judged: highAvailability.mode none renders none.
	unspread     []string
	checkedCount int
	// unread holds each namespace whose StatefulSets could not be read, with
	// the cause, and unreadErrs the errors themselves.
	unread       []string
	unreadErrs   []error
	rollingCount int
	// rollingUnderReplicated bounds the rollout skip, as in Tier-1.
	rollingUnderReplicated int
	// unobserved holds the assessed StatefulSets whose pods or rollout start
	// could not be read, so an unreadable object cannot masquerade as a clean
	// placement or rollout result.
	unobserved []string
}

func (s *tier2Scan) warn(msg string) {
	printWarning(s.log, msg)
	s.state.Warnings = append(s.state.Warnings, "Tier-2 StatefulSets: "+msg)
}

func (s *tier2Scan) fail(format string, args ...any) {
	s.failures = append(s.failures, fmt.Sprintf(format, args...))
}

func (s *tier2Scan) scanNamespace(ctx context.Context, client kubernetes.Interface, ns string) {
	stsList, err := observe(ctx, func(c context.Context) (*appsv1.StatefulSetList, error) {
		return client.AppsV1().StatefulSets(ns).List(c, metav1.ListOptions{})
	})
	if err != nil {
		// As in Tier-1: an unread namespace must not reach the trivial-pass
		// exit, and the scan keeps going so failures already observed count.
		printWarning(s.log, readFailure("StatefulSets in "+ns, err))
		s.unread = append(s.unread, fmt.Sprintf("%s (%v)", ns, err))
		s.unreadErrs = append(s.unreadErrs, err)
		return
	}
	for i := range stsList.Items {
		s.assess(ctx, client, ns, &stsList.Items[i])
	}
}

func (s *tier2Scan) assess(ctx context.Context, client kubernetes.Interface, ns string, sts *appsv1.StatefulSet) {
	if sts.Spec.Replicas == nil {
		return
	}
	want := *sts.Spec.Replicas
	known := isKnownQuorumComponent(ns, sts.Name)
	spread := spreadsAcrossNodes(sts)
	switch {
	case known && want == 0:
		// Scaled to zero is the component down, however it was installed.
		s.fail("%s/%s: scaled to zero replicas", ns, sts.Name)
		s.checkedCount++
		return
	case known && want < minQuorumSize && spread:
		// Anti-affinity is what an HA mode renders, and an HA mode runs at
		// least three members: this one has lost its quorum.
		s.fail("%s/%s: spec.replicas=%d, below the quorum minimum of %d for a StatefulSet spread for "+
			"high availability (its pods carry anti-affinity)", ns, sts.Name, want, minQuorumSize)
		s.checkedCount++
		return
	case !known && (want < minQuorumSize || want%2 == 0):
		// Not a quorum shape this check can reason about for an unknown
		// workload, so record it rather than assess it. The known components
		// are assessed at any size: a 4-node Cassandra ring at 0/4 is down,
		// whatever its parity, and a single replica down is the component down.
		s.skippedParity = append(s.skippedParity, fmt.Sprintf("%s/%s (replicas=%d)", ns, sts.Name, want))
		return
	}
	if !spread {
		s.unspread = append(s.unspread, fmt.Sprintf("%s/%s (replicas=%d)", ns, sts.Name, want))
	}

	oneDownRolling, decided := s.assessRollout(ns, sts, want)
	if decided {
		return
	}
	s.checkedCount++
	if sts.Status.ReadyReplicas < want && !oneDownRolling {
		s.fail("%s/%s: readyReplicas=%d (need %d)", ns, sts.Name, sts.Status.ReadyReplicas, want)
		return
	}
	if !oneDownRolling && !spread {
		// Ready, and its placement is not something it asked for.
		return
	}

	selector := metav1.FormatLabelSelector(sts.Spec.Selector)
	pods, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
		return client.CoreV1().Pods(ns).List(c, metav1.ListOptions{LabelSelector: selector})
	})
	if err != nil {
		// Not a placement failure: we could not look. Recording it in failures
		// would report a broken quorum for an RBAC gap on pods, while the
		// identical gap on statefulsets is correctly reported as unknown.
		s.warn(fmt.Sprintf("%s/%s: %s", ns, sts.Name, readFailure("its pods", err)))
		s.unobserved = append(s.unobserved, ns+"/"+sts.Name)
		return
	}

	// A rollout with one pod down is tolerated only while it can still
	// finish. A StatefulSet has no progress deadline, so the pods are the
	// evidence: one that cannot start, one the rollout will not replace, or
	// no progress at all for too long.
	if oneDownRolling {
		revisionCreated, revErr := updateRevisionCreated(ctx, client, ns, sts)
		reason, undated := stalledRollout(sts, pods.Items, revisionCreated, time.Now())
		switch {
		case reason != "":
			s.fail("%s/%s: readyReplicas=%d (need %d), rolling update is not progressing: %s",
				ns, sts.Name, sts.Status.ReadyReplicas, want, reason)
			return
		case undated:
			// A pod is missing and nothing dates the rollout, so how long it
			// has been missing is unknown: tolerating it would be unbounded.
			s.warn(fmt.Sprintf("%s/%s: a pod is missing and when its rollout began is unknown: %s", ns, sts.Name,
				readFailure("ControllerRevision "+ns+"/"+sts.Status.UpdateRevision, revErr)))
			s.unobserved = append(s.unobserved, ns+"/"+sts.Name)
			return
		}
		s.rollingUnderReplicated++
	}
	if spread {
		s.assessPlacement(ns, sts, pods.Items)
	}
}

// spreadsAcrossNodes reports whether sts asks for its pods on distinct nodes,
// through pod anti-affinity or a hostname topology spread. The stack renders
// anti-affinity on its quorum StatefulSets only under highAvailability.mode
// preferred or enforced, so this tells an HA install from a non-HA one
// without being told the mode, and follows an operator's own placement
// override too.
func spreadsAcrossNodes(sts *appsv1.StatefulSet) bool {
	spec := &sts.Spec.Template.Spec
	if a := spec.Affinity; a != nil && a.PodAntiAffinity != nil &&
		(len(a.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 ||
			len(a.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0) {
		return true
	}
	for i := range spec.TopologySpreadConstraints {
		if spec.TopologySpreadConstraints[i].TopologyKey == corev1.LabelHostname {
			return true
		}
	}
	return false
}

// assessRollout handles a RollingUpdate in flight. StatefulSets roll one pod
// at a time, so readyReplicas == want-1 is the steady state for the whole of
// any image bump, PVC resize or node drain, and is tolerated while the rollout
// can finish (oneDownRolling). More than one pod down fails, which decides the
// StatefulSet. Under OnDelete the controller never advances CurrentRevision on
// its own, so a revision mismatch says nothing and readiness is assessed
// directly.
func (s *tier2Scan) assessRollout(ns string, sts *appsv1.StatefulSet, want int32) (oneDownRolling, decided bool) {
	if sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType ||
		sts.Status.UpdateRevision == "" || sts.Status.CurrentRevision == sts.Status.UpdateRevision {
		return false, false
	}
	if partition := rollingUpdatePartition(sts); partition > 0 &&
		int(sts.Status.UpdatedReplicas) >= int(want)-partition && sts.Status.ReadyReplicas >= want {
		// A rollout staged with a partition has updated every pod it will
		// until the partition is lowered, and CurrentRevision does not move
		// until then. With every pod Ready nothing is in progress, so this is
		// no rollout to wait for.
		printInfo(s.log, fmt.Sprintf("  %s/%s: rollout held at partition %d (%d of %d pods updated)",
			ns, sts.Name, partition, sts.Status.UpdatedReplicas, want))
		return false, false
	}
	s.warn(fmt.Sprintf("%s/%s: %s (ready: %d/%d)",
		ns, sts.Name, RollingUpdateMarker, sts.Status.ReadyReplicas, want))
	s.rollingCount++
	switch {
	case sts.Status.ReadyReplicas >= want:
		return false, false
	case sts.Status.ReadyReplicas == want-1:
		return true, false
	default:
		s.fail("%s/%s: readyReplicas=%d (need %d, %s)",
			ns, sts.Name, sts.Status.ReadyReplicas, want, RollingUpdateMarker)
		s.checkedCount++
		return false, true
	}
}

// assessPlacement fails two Ready peers on one node: one node loss from losing
// quorum. Only Ready pods owned by this StatefulSet count; phase stays Running
// through CrashLoopBackOff, and a surplus pod left over from a rollout would
// otherwise read as a co-location. A tolerated rollout is scanned too.
func (s *tier2Scan) assessPlacement(ns string, sts *appsv1.StatefulSet, pods []corev1.Pod) {
	nodeOwner := make(map[string]string)
	for j := range pods {
		p := &pods[j]
		if !metav1.IsControlledBy(p, sts) || !isPodReady(p) {
			continue
		}
		if first, dup := nodeOwner[p.Spec.NodeName]; dup {
			s.fail("%s/%s: pods %s and %s are co-located on node %s", ns, sts.Name, first, p.Name, p.Spec.NodeName)
		} else {
			nodeOwner[p.Spec.NodeName] = p.Name
		}
	}
}

func (s *tier2Scan) setOK(ok bool) {
	s.state.Tier2StatefulSetsOK = &ok
}

func (s *tier2Scan) deniedWarning() string {
	return fmt.Sprintf("Tier-2 StatefulSets: could not read StatefulSets in %d control-plane namespace(s): %s",
		len(s.unread), readFailures(s.unread, s.unreadErrs))
}

func (s *tier2Scan) verdict() {
	if len(s.unread) > 0 {
		// As in Tier-1: reported whatever decides the row.
		s.state.Warnings = append(s.state.Warnings, s.deniedWarning())
	}
	if len(s.unspread) > 0 {
		printInfo(s.log, fmt.Sprintf("  Placement not assessed (no pod anti-affinity, as highAvailability.mode "+
			"none renders): %s", strings.Join(s.unspread, ", ")))
	}
	if len(s.skippedParity) > 0 {
		s.warn(fmt.Sprintf("%d StatefulSet(s) not assessed (not a quorum shape this check covers): %s",
			len(s.skippedParity), strings.Join(s.skippedParity, ", ")))
	}
	if s.checkedCount == 0 {
		s.verdictNothingAssessed()
		return
	}
	switch {
	case len(s.failures) > 0:
		printError(s.log, fmt.Sprintf("Tier-2 quorum/placement findings (%d):", len(s.failures)))
		for _, f := range s.failures {
			printInfo(s.log, "  "+f)
		}
		s.state.Recommendations = append(s.state.Recommendations,
			"Ensure each Tier-2 StatefulSet (NATS, OpenBao, Cassandra) has all spec.replicas pods Ready "+
				"and spread across distinct nodes.")
		s.setOK(false)
	case len(s.unread) > 0:
		printWarning(s.log, fmt.Sprintf("%d quorum StatefulSet(s) healthy, but %d namespace(s) were not readable",
			s.checkedCount, len(s.unread)))
	case len(s.unobserved) > 0:
		printWarning(s.log, fmt.Sprintf("%d quorum StatefulSet(s) assessed, but %s could not be observed",
			s.checkedCount, strings.Join(s.unobserved, ", ")))
		s.state.Warnings = append(s.state.Warnings, fmt.Sprintf(
			"Tier-2 StatefulSets: status unknown (%s could not be observed)", strings.Join(s.unobserved, ", ")))
	case s.rollingUnderReplicated > 0:
		// Tolerated, not unknown: same rule as Tier-1. A NATS StatefulSet at
		// 2/3 mid-RollingUpdate is what an upgrade looks like, and reporting
		// it as an unobserved critical check made every control-plane upgrade
		// NVCF-Not-Ready with a non-zero exit.
		s.warn(fmt.Sprintf("%d quorum StatefulSet(s) healthy, %d %s and below target",
			s.checkedCount, s.rollingUnderReplicated, MidRolloutMarker))
		s.setOK(true)
	default:
		if s.rollingCount > 0 {
			printWarning(s.log, fmt.Sprintf(
				"%d quorum StatefulSet(s) healthy, %d %s but at their replica target",
				s.checkedCount, s.rollingCount, MidRolloutMarker))
		}
		printSuccess(s.log, fmt.Sprintf("All %d quorum StatefulSet(s) Ready on distinct nodes", s.checkedCount))
		s.setOK(true)
	}
}

func (s *tier2Scan) verdictNothingAssessed() {
	switch {
	case len(s.unread) > 0:
		printWarning(s.log, fmt.Sprintf("StatefulSets not readable in %d control-plane namespace(s)", len(s.unread)))
	case len(s.skippedParity) > 0:
		// StatefulSets exist in the quorum namespaces but none was assessed.
		// Claiming "Quorum and Placement OK" here would certify a ring this
		// check never looked at.
		s.state.Warnings = append(s.state.Warnings,
			"Tier-2 StatefulSets: status unknown (no quorum-shaped StatefulSet was assessed)")
	case s.state.PostInstall:
		// Installed, yet no quorum component is here. The stack can leave
		// NATS, OpenBao and Cassandra to run elsewhere, so this is a warning
		// rather than a failure, but it must not read as a clean pass.
		s.warn("no quorum StatefulSet (NATS, OpenBao, Cassandra) found in the control-plane namespaces after " +
			"install; expected only if they run outside this cluster")
		s.setOK(true)
	default:
		printInfo(s.log, "  No quorum StatefulSets found (pre-install state)")
		s.setOK(true)
	}
}

// checkConfigurableReachability probes user-defined endpoints loaded from the
// cluster-validator ConfigMap.
func checkConfigurableReachability(ctx context.Context, state *ValidationState, cfg *ReachabilityConfig) {
	log := state.Log
	printHeader(log, "Endpoint Reachability Checks")
	printInfo(log, "Testing configured endpoints...")

	allOK := true
	hasCritical := false
	allCriticalOK := true

	// Per-endpoint results for the metrics pipeline. The agent emits one
	// Prometheus gauge per entry; the map key becomes the `endpoint=...`
	// label value.
	if state.EndpointResults == nil {
		state.EndpointResults = make(map[string]EndpointResult, len(cfg.Endpoints))
	}

	for _, ep := range cfg.Endpoints {
		target := toEndpoint(ep)
		display := target.DisplayAddr()

		if ep.Critical {
			hasCritical = true
		}

		// Surface the implicit https->tcp+tls fallback so the operator
		// can see that the probe protocol differs from what they wrote.
		if ep.Protocol == protocolHTTPS && ep.URL == "" && target.Protocol == protocolTCPTLS {
			printInfo(log, fmt.Sprintf(
				"  %s: https without 'url'; probing %s via tcp+tls", ep.Name, display))
		}

		// Pre-flight: surface a clear diagnostic when the endpoint config
		// is missing fields required by its protocol, instead of letting
		// it fall through to a silent "Not Reachable" that's
		// indistinguishable from a real connectivity failure.
		if reason := unprobableReason(target); reason != "" {
			allOK = false
			state.EndpointResults[ep.Name] = EndpointResult{Reachable: false, Critical: ep.Critical}
			msg := fmt.Sprintf("  %s: %s: %s (treated as unreachable)", ep.Name, display, reason)
			if ep.Critical {
				allCriticalOK = false
				printError(log, msg)
			} else {
				printWarning(log, msg)
			}
			continue
		}

		if TestEndpoint(ctx, target) {
			state.EndpointResults[ep.Name] = EndpointResult{Reachable: true, Critical: ep.Critical}
			printSuccess(log, fmt.Sprintf("  %s: %s - Reachable", ep.Name, display))
		} else {
			allOK = false
			state.EndpointResults[ep.Name] = EndpointResult{Reachable: false, Critical: ep.Critical}
			if ep.Critical {
				allCriticalOK = false
				printError(log, fmt.Sprintf("  %s: %s - Not Reachable (critical)", ep.Name, display))
			} else {
				printWarning(log, fmt.Sprintf("  %s: %s - Not Reachable", ep.Name, display))
			}
		}
	}

	result := allOK
	state.ReachabilityOK = &result
	if hasCritical {
		state.ReachabilityCriticalOK = &allCriticalOK
	}
	log.Info("")
	if allOK {
		printSuccess(log, "All endpoint reachability checks passed")
	} else if !allCriticalOK {
		printError(log, "One or more critical endpoints are not reachable")
		// Don't assume egress is the cause: DNS resolution failures (typo
		// in hostname) and wrong-environment URLs (e.g. prod endpoint on a
		// staging cluster) look identical to a real egress block here.
		// Cover all three root causes in one actionable line.
		state.Recommendations = append(state.Recommendations,
			"For each unreachable endpoint above, verify (1) the hostname and port "+
				"are correct for this cluster's environment (no typos; correct "+
				"staging vs. production URL), and (2) cluster egress permits "+
				"traffic to it (NetworkPolicy, firewall, proxy).")
	} else {
		printWarning(log, "One or more endpoints are not reachable (non-critical)")
		state.Warnings = append(state.Warnings,
			"Reachability: One or more endpoints not reachable")
	}
}

func toEndpoint(ep ReachabilityEndpoint) Endpoint {
	out := Endpoint{
		URL:      ep.URL,
		Host:     ep.Host,
		Port:     ep.Port,
		Protocol: ep.Protocol,
	}
	// HTTPS without an explicit URL: fall back to a TCP+TLS handshake
	// against host:port. The chart schema permits omitting `url` when
	// host is set, and tcp+tls is the equivalent probe, the same
	// host:port already works as `protocol: tcp+tls`. Without this
	// fallback, testHTTPS("") was being called and always returning
	// false, producing a silent "Not Reachable" indistinguishable from
	// a real connectivity failure.
	if out.Protocol == protocolHTTPS && out.URL == "" && out.Host != "" {
		if out.Port == 0 {
			out.Port = 443
		}
		out.Protocol = protocolTCPTLS
	}
	return out
}

// unprobableReason returns a non-empty diagnostic when an endpoint cannot
// be probed because required fields for its declared protocol are missing.
// An empty return means the endpoint config is sufficient.
func unprobableReason(ep Endpoint) string {
	switch ep.Protocol {
	case protocolHTTPS:
		// toEndpoint() derives URL from host:port for https when host is
		// set; reaching here means BOTH url and host are empty.
		if ep.URL == "" {
			return "missing 'url' (or 'host') for https probe"
		}
	case protocolTCP, protocolTCPTLS:
		if ep.Host == "" || ep.Port == 0 {
			return fmt.Sprintf("missing 'host' or 'port' for %s probe", ep.Protocol)
		}
	}
	return ""
}

// versionGTE checks if semantic version v1 >= v2.
func versionGTE(v1, v2 string) bool {
	p1 := parseVersion(strings.TrimPrefix(v1, "v"))
	p2 := parseVersion(strings.TrimPrefix(v2, "v"))
	if p1 == nil || p2 == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if p1[i] > p2[i] {
			return true
		}
		if p1[i] < p2[i] {
			return false
		}
	}
	return true
}

func parseVersion(v string) []int {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 3 {
		return nil
	}
	result := make([]int, 3)
	for i, p := range parts {
		// Strip pre-release suffixes (e.g. "0-rc1").
		if idx := strings.IndexAny(p, "-+"); idx >= 0 {
			p = p[:idx]
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		result[i] = n
	}
	return result
}

// knownQuorumComponents are the Tier-2 StatefulSets whose whole purpose is a
// quorum, by the names the stack's releases give them in their default
// namespaces. Matching on the namespace as well keeps a same-prefixed
// workload such as cassandra-backup, or a nats elsewhere, out of the rule.
var knownQuorumComponents = map[string][]string{
	"nats-system":      {"nats"},
	"vault-system":     {"openbao", "openbao-server"},
	"cassandra-system": {"cassandra"},
}

// stuckWaitingReasons are container waiting reasons that do not clear on their
// own, so a pod showing one is not a rollout step in progress.
var stuckWaitingReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

// stalledRolloutRestarts is how many restarts mark a rolled pod as failing
// rather than starting. A healthy replacement comes up without restarting.
const stalledRolloutRestarts = 3

// stalledRolloutAfter bounds how long a one-down rollout may go without
// progress: no pod created or deleted and no new update revision. The pod
// reasons below catch the stalls that announce themselves; this catches the
// rest, such as a replacement that is never created (refused by admission or
// a quota), one stuck in ContainerCreating, or a member that never joins.
var stalledRolloutAfter = 15 * time.Minute

// stalledRollout reports why a one-down StatefulSet rollout cannot finish, or
// "" when it still can. revisionCreated is when the update revision was
// created, which dates the start of the rollout; zero when it is unknown.
// undated reports that a pod is missing and nothing dates the rollout, so
// whether it stalled cannot be told.
func stalledRollout(
	sts *appsv1.StatefulSet, pods []corev1.Pod, revisionCreated, now time.Time,
) (reason string, undated bool) {
	lastProgress := revisionCreated
	owned := 0
	for i := range pods {
		p := &pods[i]
		if !metav1.IsControlledBy(p, sts) {
			continue
		}
		owned++
		lastProgress = laterOf(lastProgress, p.CreationTimestamp.Time)
		if p.DeletionTimestamp != nil {
			// The controller deletes each pod it replaces, whatever its
			// revision or readiness, so a terminating pod is the rollout
			// moving. The time bound covers one that never finishes.
			lastProgress = laterOf(lastProgress, p.DeletionTimestamp.Time)
			continue
		}
		if isPodReady(p) {
			continue
		}
		if reason := stalledPodReason(sts, p); reason != "" {
			return reason, false
		}
	}
	if owned < int(*sts.Spec.Replicas) && revisionCreated.IsZero() {
		// A pod is missing and when the rollout began is unknown. A missing
		// pod is usually the controller between deleting a pod and recreating
		// it, but one that is never recreated would be tolerated forever.
		return "", true
	}
	if lastProgress.IsZero() {
		return "", false
	}
	if idle := now.Sub(lastProgress); idle > stalledRolloutAfter {
		return fmt.Sprintf("no progress for %s: no pod created or deleted, and no new revision, since %s",
			idle.Round(time.Minute), lastProgress.UTC().Format(time.RFC3339)), false
	}
	return "", false
}

// stalledPodReason reports why a down pod of a rolling StatefulSet will not
// become Ready on its own, or "" when it may still.
func stalledPodReason(sts *appsv1.StatefulSet, p *corev1.Pod) string {
	// A partition holds pods below it on their old revision: the rollout
	// will not replace one that is down there, so it stays down.
	if rev := p.Labels[appsv1.ControllerRevisionHashLabelKey]; rev != "" && rev != sts.Status.UpdateRevision {
		if partition := rollingUpdatePartition(sts); partition > 0 {
			if ordinal := podOrdinal(sts, p); ordinal >= 0 && ordinal < partition {
				return fmt.Sprintf("pod %s is down below the rollout partition %d, so the rollout will not replace it",
					p.Name, partition)
			}
		}
	}
	if p.Status.Phase == corev1.PodFailed {
		return fmt.Sprintf("pod %s failed", p.Name)
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return fmt.Sprintf("pod %s cannot be scheduled: %s", p.Name, c.Reason)
		}
	}
	statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...),
		p.Status.ContainerStatuses...)
	for _, cs := range statuses {
		if w := cs.State.Waiting; w != nil && stuckWaitingReasons[w.Reason] {
			return fmt.Sprintf("pod %s container %s is in %s", p.Name, cs.Name, w.Reason)
		}
		if cs.RestartCount >= stalledRolloutRestarts {
			return fmt.Sprintf("pod %s container %s has restarted %d times", p.Name, cs.Name, cs.RestartCount)
		}
	}
	return ""
}

// rollingUpdatePartition is the ordinal below which a RollingUpdate leaves
// pods on their old revision, 0 when there is none.
func rollingUpdatePartition(sts *appsv1.StatefulSet) int {
	if ru := sts.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
		return int(*ru.Partition)
	}
	return 0
}

// podOrdinal is a StatefulSet pod's ordinal, from its pod-index label or its
// name, or -1 when neither gives one.
func podOrdinal(sts *appsv1.StatefulSet, p *corev1.Pod) int {
	raw, ok := p.Labels[appsv1.PodIndexLabel]
	if !ok {
		var found bool
		raw, found = strings.CutPrefix(p.Name, sts.Name+"-")
		if !found {
			return -1
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// updateRevisionCreated is when the StatefulSet's update revision was
// created, which is when its current rollout began, or the zero time and why
// the revision could not be read.
func updateRevisionCreated(
	ctx context.Context, client kubernetes.Interface, ns string, sts *appsv1.StatefulSet,
) (time.Time, error) {
	if sts.Status.UpdateRevision == "" {
		return time.Time{}, errors.New("the StatefulSet reports no update revision")
	}
	rev, err := observe(ctx, func(c context.Context) (*appsv1.ControllerRevision, error) {
		return client.AppsV1().ControllerRevisions(ns).Get(c, sts.Status.UpdateRevision, metav1.GetOptions{})
	})
	if err != nil {
		return time.Time{}, err
	}
	return rev.CreationTimestamp.Time, nil
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// isKnownQuorumComponent reports whether the StatefulSet ns/name is one of the
// stack's quorum components. The OpenBao namespace follows its override.
// A relocated OpenBao is looked up under vault-system; vault-system itself is
// then not scanned (controlPlaneNamespaceSet replaces it).
func isKnownQuorumComponent(ns, name string) bool {
	if v := strings.TrimSpace(os.Getenv(openBaoNamespaceEnv)); v != "" && ns == v {
		ns = "vault-system"
	}
	for _, c := range knownQuorumComponents[ns] {
		if name == c {
			return true
		}
	}
	return false
}
