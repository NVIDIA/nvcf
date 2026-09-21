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
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/discovery"
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
		log.Errorf("║              %s  Cluster is NVCF-Not-Ready  %s              ║", iconCross, iconCross)
		log.Error("╚═══════════════════════════════════════════════════════════╝")
		return fmt.Errorf("cluster not reachable")
	}

	printSuccess(log, "Connected to Kubernetes cluster")
	log.Info("")
	log.Info("Cluster Information:")
	state.K8sVersion = sv.GitVersion
	printInfo(log, fmt.Sprintf("  Kubernetes version: %s", state.K8sVersion))

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err == nil {
		state.TotalNodes = strconv.Itoa(len(nodes.Items))
	} else {
		state.TotalNodes = "0"
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
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		printError(log, fmt.Sprintf("Failed to list nodes: %v", err))
		podsHealthy = false
		// Node readiness is unknown — reflect that in the summary row so
		// it doesn't read "Worker Nodes: All Ready" when we never checked.
		nodesAllReady = false
		state.NodesAllReady = false
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
	hasMutating, hasValidating := discoverWebhookAPIs(client.Discovery())

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

func discoverWebhookAPIs(disco discovery.DiscoveryInterface) (hasMutating, hasValidating bool) {
	resources, err := disco.ServerResourcesForGroupVersion("admissionregistration.k8s.io/v1")
	if err != nil {
		return false, false
	}
	for _, r := range resources.APIResources {
		switch r.Name {
		case "mutatingwebhookconfigurations":
			hasMutating = true
		case "validatingwebhookconfigurations":
			hasValidating = true
		}
	}
	return hasMutating, hasValidating
}

// checkNetworkPolicies verifies that the NetworkPolicy API is available and
// attempts to detect a known CNI plugin.
func checkNetworkPolicies(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Network Policy Support")
	supportsNetpol := false

	resources, err := client.Discovery().ServerResourcesForGroupVersion("networking.k8s.io/v1")
	if err != nil {
		printError(log, "NetworkPolicy API is not available")
		state.Recommendations = append(state.Recommendations,
			"Ensure Kubernetes cluster supports networking.k8s.io API group")
		return
	}

	found := false
	for _, r := range resources.APIResources {
		if r.Name == "networkpolicies" {
			found = true
			break
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

	for _, cni := range cniChecks {
		pods, err := client.CoreV1().Pods(cni.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: cni.Label,
		})
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
		netpols, err := client.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{})
		if err == nil && len(netpols.Items) > 0 {
			printInfo(log, "Existing NetworkPolicies found in cluster")
			supportsNetpol = true
		} else {
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

	_, err := client.StorageV1().CSIDrivers().Get(ctx, "smb.csi.k8s.io", metav1.GetOptions{})
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
	printHeader(log, "GPU Resources")

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		printWarning(log, "Could not retrieve node information")
		state.Recommendations = append(state.Recommendations,
			"Add GPU nodes to the cluster or verify GPU Operator is functioning")
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

	_, err := client.CoreV1().Namespaces().Get(ctx, gpuOperatorNS, metav1.GetOptions{})
	if err == nil {
		printSuccess(log, fmt.Sprintf("GPU Operator namespace exists: %s", gpuOperatorNS))

		pods, err := client.CoreV1().Pods(gpuOperatorNS).List(ctx, metav1.ListOptions{})
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
		pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
			LabelSelector: "app=gpu-operator",
		})
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

	classes, err := client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		// Leave DefaultStorageClassOK nil (unknown) so the summary row is
		// omitted rather than reported as "Not Found" — an API error is not
		// confirmation that no default StorageClass exists.
		printWarning(log, fmt.Sprintf("Could not list StorageClasses: %v", err))
		state.Warnings = append(state.Warnings, "Default StorageClass: status unknown (listing failed)")
		return
	}

	// Collect every default rather than stopping at the first. Two classes
	// annotated is-default-class reject all PVCs on Kubernetes <1.26, and on
	// >=1.26 the apiserver picks the newest, which need not be the one listed
	// first here.
	var defaults []string
	for _, sc := range classes.Items {
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
			sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
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
				"Multiple default StorageClasses found (%s); Kubernetes >= %s binds PVCs with the newest, but the extras should be cleared",
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
		printError(log, fmt.Sprintf("No default StorageClass found (%d classes present, none marked as default)", len(classes.Items)))
		state.Recommendations = append(state.Recommendations,
			"Mark a StorageClass as default with: "+
				"kubectl patch storageclass <name> -p '{\"metadata\":{\"annotations\":{\"storageclass.kubernetes.io/is-default-class\":\"true\"}}}'")
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
	// envoyGatewayControllerSelector matches the controller Deployment's pods
	// only, excluding the data-plane proxies and certgen Job in the same namespace.
	envoyGatewayControllerSelector = "control-plane=envoy-gateway"
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
func discoverGatewayAPIResources(client kubernetes.Interface) (gatewayAPISurface, error) {
	surface := gatewayAPISurface{
		byGroupVersion: make(map[string]bool),
		anyVersion:     make(map[string]bool),
	}
	groups, err := client.Discovery().ServerGroups()
	if err != nil {
		return surface, err
	}
	for _, g := range groups.Groups {
		if g.Name != gatewayAPIGroup {
			continue
		}
		for _, v := range g.Versions {
			resources, err := client.Discovery().ServerResourcesForGroupVersion(v.GroupVersion)
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

// checkGatewayAPICRDs verifies that the Gateway API CRD set is installed and
// registers all four required resource types. Without these CRDs neither the
// Gateway controller nor nvcf-cli can create routing objects.
func checkGatewayAPICRDs(_ context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Gateway API CRDs")

	surface, err := discoverGatewayAPIResources(client)
	if err != nil {
		// Leave the pointer nil: discovery failure is not evidence the CRDs
		// are absent, and this row is critical.
		printWarning(log, fmt.Sprintf("Could not discover Gateway API resources: %v", err))
		state.Warnings = append(state.Warnings,
			"Gateway API CRDs: status unknown (API group discovery failed)")
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
// at least one running pod in the envoy-gateway-system namespace. Without a
// running gateway controller, Gateway and HTTPRoute objects are never reconciled
// and no traffic reaches NVCF services.
func checkEnvoyGateway(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Envoy Gateway")

	envoyNS := envoyGatewayNamespaceName()
	_, err := client.CoreV1().Namespaces().Get(ctx, envoyNS, metav1.GetOptions{})
	if err != nil {
		// Only NotFound is evidence that Envoy is absent. A 403 or an apiserver
		// 500 means we never observed it, so leave the pointer nil and warn,
		// matching every sibling control-plane check.
		if !apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("Could not check Envoy Gateway namespace %s: %v", envoyNS, err)
			printWarning(log, msg)
			state.Warnings = append(state.Warnings, "Envoy Gateway: status unknown ("+msg+")")
			return
		}
		printError(log, fmt.Sprintf("Envoy Gateway namespace %s not found", envoyNS))
		state.Recommendations = append(state.Recommendations,
			"Install Envoy Gateway via the NVCF self-managed stack (nvcf-cli up) or "+
				"helm install eg oci://docker.io/envoyproxy/gateway-helm -n envoy-gateway-system --create-namespace")
		ok := false
		state.EnvoyGatewayOK = &ok
		return
	}

	// Select on the controller label: the same namespace also holds the
	// envoy-<ns>-<gw>-<hash> data-plane proxies and the certgen Job pod, and
	// counting those lets a dead controller pass.
	pods, err := client.CoreV1().Pods(envoyNS).List(ctx, metav1.ListOptions{
		LabelSelector: envoyGatewayControllerSelector,
	})
	if err != nil {
		// Same reasoning as the namespace Get above: a List failure is not
		// evidence that no controller is running.
		msg := fmt.Sprintf("Could not list Envoy Gateway pods in %s: %v", envoyNS, err)
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Envoy Gateway: status unknown ("+msg+")")
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
		printError(log, fmt.Sprintf("No Ready Envoy Gateway controller pods in %s (%d found)",
			envoyNS, len(pods.Items)))
		ok := false
		state.EnvoyGatewayOK = &ok
		return
	}

	printSuccess(log, fmt.Sprintf("Envoy Gateway: %d controller pod(s) Ready in %s", ready, envoyNS))
	ok := true
	state.EnvoyGatewayOK = &ok
}

// checkGatewayRoutes verifies that the route CR types NVCF creates are
// registered with the apiserver. It does discovery only: it does not list
// route objects, so it cannot tell whether any route actually exists.
//
// Non-critical: route CR types are installed by nvcf up and are expected to
// be absent on a fresh cluster before install.
func checkGatewayRoutes(_ context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Gateway Route CR Types")

	surface, err := discoverGatewayAPIResources(client)
	if err != nil {
		// Leave the pointer nil: a discovery failure is not evidence that the
		// route CR types are absent.
		printWarning(log, fmt.Sprintf("Could not discover Gateway API resources: %v", err))
		state.Warnings = append(state.Warnings,
			"Gateway Route CR Types: status unknown (API group discovery failed)")
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

// checkExternalLoadBalancer performs a passive check: it lists Services of type
// LoadBalancer in the gateway namespace and looks for one with a populated
// .status.loadBalancer.ingress. A populated ingress means a load balancer
// controller (cloud LB, MetalLB, etc.) is active and assigned an IP or hostname.
//
// Non-critical: the passive form only detects an existing LB service; it does
// not create a probe service, so absence means either no LB service exists yet
// or no LB controller is installed.
func checkExternalLoadBalancer(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "External Load Balancer")

	// Scope to the gateway namespace. An unscoped list is satisfied by any
	// LoadBalancer anywhere (ingress-nginx, a demo app), which masks the NVCF
	// gateway's own Service sitting at <pending> on an exhausted address pool.
	envoyNS := envoyGatewayNamespaceName()
	services, err := client.CoreV1().Services(envoyNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Leave the pointer nil: a List failure is not evidence that no
		// LoadBalancer has an address.
		printWarning(log, fmt.Sprintf("Could not list services in %s: %v", envoyNS, err))
		state.Warnings = append(state.Warnings,
			"External Load Balancer: status unknown (Service listing failed)")
		return
	}

	type lbResult struct {
		name      string
		namespace string
		addr      string
	}
	var found []lbResult
	// Envoy Gateway provisions one proxy Service per Gateway, and the stack
	// defines several. Tracking the unassigned ones stops a partially
	// satisfied address pool from passing on the strength of its siblings.
	var pending []string
	for i := range services.Items {
		svc := &services.Items[i]
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		addr := ""
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			if addr = ing.IP; addr == "" {
				addr = ing.Hostname
			}
			if addr != "" {
				break
			}
		}
		if addr == "" {
			pending = append(pending, svc.Namespace+"/"+svc.Name)
			continue
		}
		found = append(found, lbResult{svc.Name, svc.Namespace, addr})
	}

	if len(pending) > 0 {
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
		printWarning(log, "No LoadBalancer Services with an assigned external address found")
		printInfo(log, "  This may indicate: no LB controller is installed (MetalLB, cloud LB), "+
			"or no LoadBalancer Service exists yet (normal before nvcf-cli up)")
		state.Warnings = append(state.Warnings,
			"External Load Balancer: no Service of type LoadBalancer has an assigned external IP or hostname. "+
				"Verify a load balancer controller is installed.")
		ok := false
		state.ExternalLBOK = &ok
		return
	}

	printSuccess(log, fmt.Sprintf("%d LoadBalancer Service(s) with external address:", len(found)))
	for _, svc := range found {
		printInfo(log, fmt.Sprintf("  %s/%s → %s", svc.namespace, svc.name, svc.addr))
	}
	ok := true
	state.ExternalLBOK = &ok
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
	nodeToNodeDSTimeout      = 2 * time.Minute
	nodeToNodeStatusTimeout  = 30 * time.Second
	nodeToNodeCheckerTimeout = 90 * time.Second
	// orphanN2NNamespaceTTL is the minimum age before a leftover
	// nvcf-n2n-validation-* namespace is swept. Must exceed the sum of the
	// DaemonSet and checker timeouts to avoid racing a concurrent run.
	orphanN2NNamespaceTTL = 10 * time.Minute
)

// nodeToNodeProbeImage resolves the probe image, honouring the same
// enforcement.testImage override the sibling NetworkPolicy probe uses. Without
// it, an air-gapped or registry-mirrored cluster ImagePullBackOffs on every
// DaemonSet pod and the timeout is reported as an overlay fault.
func nodeToNodeProbeImage(cfg *NetworkCheckConfig) string {
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
// probe namespace created their DaemonSet. Kept so an orphan left by a
// currently deployed validator is still reclaimable; remove once those
// versions are out of service.
const legacyNodeToNodeNamespace = "default"

// sweepLegacyOrphanN2NDaemonSets reclaims probe DaemonSets stranded in
// "default" by an older validator that was killed before its cleanup ran.
// The per-run namespace sweep cannot see those: they predate the namespace.
// Without this they persist indefinitely, one probe pod per node.
func sweepLegacyOrphanN2NDaemonSets(ctx context.Context, log *logrus.Entry, client kubernetes.Interface, ttl time.Duration) {
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

// checkNodeToNode verifies overlay-network connectivity across all schedulable
// nodes using a DaemonSet-based probe. A server DaemonSet is deployed on every
// schedulable node; a checker pod on node[0] connects to each server pod IP on
// nodes[1..N-1].
//
// The topology is a single-source star, not a full mesh: it proves node[0]
// reaches every other node, which catches a dead overlay and most single-node
// isolation. It does not prove node[i] reaches node[j] for i,j != 0, and it
// does not probe the reverse direction back toward node[0].
//
// This check creates a namespace, a DaemonSet, and a pod, so the ServiceAccount
// must hold create/delete on all three. A denial on any of them leaves the
// result unknown rather than failing the overlay, because a missing grant is
// not evidence that node-to-node traffic is broken.
//
// Critical: broken overlay means NVCF services on different nodes cannot
// communicate, causing cascade failures across every API call.
func checkNodeToNode(ctx context.Context, client kubernetes.Interface, state *ValidationState, image string) {
	log := state.Log
	printHeader(log, "Node-to-Node Communication")

	// Reclaim DaemonSets orphaned by prior runs killed before their deferred
	// cleanup fired (SIGKILL, OOM, node failure).
	sweepOrphanN2NNamespaces(ctx, log, client, orphanN2NNamespaceTTL)
	sweepLegacyOrphanN2NDaemonSets(ctx, log, client, orphanN2NNamespaceTTL)

	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		printWarning(log, fmt.Sprintf("Could not list nodes: %v", err))
		state.Warnings = append(state.Warnings, "Node-to-Node: status unknown (node listing failed)")
		return
	}

	var schedulable []string
	for i := range nodes.Items {
		if !nodes.Items[i].Spec.Unschedulable {
			schedulable = append(schedulable, nodes.Items[i].Name)
		}
	}

	// Leave the pointer nil rather than reporting Verified: there is no second
	// node to reach, so the overlay was not exercised. The summary renders this
	// as an explicit UNKNOWN row.
	if len(schedulable) < 2 {
		printInfo(log, fmt.Sprintf("  %d schedulable node(s); node-to-node check skipped", len(schedulable)))
		state.Warnings = append(state.Warnings,
			"Node-to-Node: skipped (fewer than 2 schedulable nodes)")
		return
	}

	suffix := rand.String(6)
	dsName := nodeToNodeDSName + "-" + suffix
	checkerName := nodeToNodeCheckerName + "-" + suffix
	dsLabels := map[string]string{
		"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
		"app.kubernetes.io/component":  "n2n-server",
		"app.kubernetes.io/instance":   suffix,
	}

	ns := nodeToNodeNSPrefix + suffix
	if err := createNodeToNodeNamespace(ctx, client, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		printWarning(log, fmt.Sprintf("Could not create probe namespace %s: %v", ns, err))
		state.Warnings = append(state.Warnings,
			"Node-to-Node: status unknown (probe namespace could not be created)")
		return
	}

	// Deleting the namespace removes the DaemonSet and checker pod with it, but
	// delete them first so a namespace stuck terminating does not strand the
	// probe pods on every node.
	defer func() {
		grace := int64(0)
		opts := metav1.DeleteOptions{GracePeriodSeconds: &grace}
		_ = client.AppsV1().DaemonSets(ns).Delete(context.Background(), dsName, opts)
		_ = client.CoreV1().Pods(ns).Delete(context.Background(), checkerName, opts)
		delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.CoreV1().Namespaces().Delete(delCtx, ns, metav1.DeleteOptions{}); err != nil &&
			!apierrors.IsNotFound(err) {
			log.Warnf("Failed to clean up probe namespace %s: %v", ns, err)
		}
	}()

	ds, err := client.AppsV1().DaemonSets(ns).Create(
		ctx, buildNodeToNodeDaemonSet(dsName, ns, dsLabels, image), metav1.CreateOptions{},
	)
	if err != nil {
		// A denial means we could not run the probe, not that the overlay is
		// broken. Without this an operator chart missing the daemonsets
		// create verb reports NVCF-Not-Ready on every CronJob tick of a
		// healthy cluster.
		if apierrors.IsForbidden(err) {
			msg := fmt.Sprintf("RBAC denied creating the probe DaemonSet in %s: %v", ns, err)
			printWarning(log, msg)
			state.Warnings = append(state.Warnings, "Node-to-Node: status unknown ("+msg+")")
			return
		}
		printError(log, fmt.Sprintf("Failed to create server DaemonSet: %v", err))
		ok := false
		state.NodeToNodeOK = &ok
		return
	}

	// DesiredNumberScheduled is the only number that accounts for taints the
	// DaemonSet has no toleration for. The Create response always carries a
	// zeroed status because the DaemonSet controller populates it
	// asynchronously, so poll for it instead of reading it off ds directly.
	// Falling back to len(schedulable) would count NoSchedule-tainted
	// control-plane nodes and fail a healthy cluster on timeout.
	wantPods, err := waitForDaemonSetDesiredCount(ctx, client, ns, ds.Name, nodeToNodeStatusTimeout)
	if err != nil {
		printWarning(log, fmt.Sprintf("Could not determine DaemonSet scheduling target: %v", err))
		state.Warnings = append(state.Warnings,
			"Node-to-Node: status unknown (DaemonSet status never reported a scheduling target)")
		return
	}
	if wantPods < 2 {
		printInfo(log, fmt.Sprintf("  DaemonSet schedulable on %d node(s); node-to-node check skipped", wantPods))
		state.Warnings = append(state.Warnings,
			"Node-to-Node: skipped (probe DaemonSet schedulable on fewer than 2 nodes)")
		return
	}

	log.Infof("  Waiting for server DaemonSet pods on %d nodes...", wantPods)
	selector := metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: dsLabels})
	// minNodes=2: two nodes is the smallest set that proves cross-node
	// traffic, so a NotReady or cordoned node counted in wantPods degrades
	// coverage rather than failing the check.
	serverPods, err := waitForDaemonSetPods(ctx, client, ns, selector, wantPods, 2, nodeToNodeDSTimeout)
	if err != nil {
		printError(log, fmt.Sprintf("Server DaemonSet pods did not become ready: %v", err))
		ok := false
		state.NodeToNodeOK = &ok
		return
	}

	// Partial coverage is a real result, but the operator has to be told the
	// probe did not reach every node it was scheduled onto.
	if len(serverPods) < wantPods {
		msg := fmt.Sprintf("probe covered %d of %d scheduled node(s); the rest never reported a Running pod",
			len(serverPods), wantPods)
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Node-to-Node: "+msg)
	}

	// Select checkerNode from a Running server pod so it is guaranteed to be
	// a node where the DaemonSet actually scheduled.
	checkerNode := serverPods[0].Spec.NodeName
	var targetIPs []string
	for i := range serverPods {
		if serverPods[i].Spec.NodeName != checkerNode && serverPods[i].Status.PodIP != "" {
			targetIPs = append(targetIPs, serverPods[i].Status.PodIP)
			log.Infof("  Server pod on %s: %s", serverPods[i].Spec.NodeName, serverPods[i].Status.PodIP)
		}
	}

	if len(targetIPs) == 0 {
		// Leave the pointer nil. Reporting a critical check as Verified having
		// sent zero packets is worse than reporting it as not run.
		printWarning(log, "No cross-node server pod IPs available; probe did not run")
		state.Warnings = append(state.Warnings,
			"Node-to-Node: status unknown (no cross-node probe targets were available)")
		return
	}

	if _, err := client.CoreV1().Pods(ns).Create(
		ctx, buildNodeToNodeCheckerPod(checkerName, ns, checkerNode, targetIPs, image), metav1.CreateOptions{},
	); err != nil {
		// Same reasoning as the DaemonSet create above.
		if apierrors.IsForbidden(err) {
			msg := fmt.Sprintf("RBAC denied creating the checker pod in %s: %v", ns, err)
			printWarning(log, msg)
			state.Warnings = append(state.Warnings, "Node-to-Node: status unknown ("+msg+")")
			return
		}
		printError(log, fmt.Sprintf("Failed to create checker pod: %v", err))
		ok := false
		state.NodeToNodeOK = &ok
		return
	}

	succeeded, err := waitForPodDone(ctx, client, ns, checkerName, nodeToNodeCheckerTimeout)
	if err != nil {
		printError(log, fmt.Sprintf("Checker pod error: %v", err))
		ok := false
		state.NodeToNodeOK = &ok
		return
	}

	if succeeded {
		printSuccess(log, fmt.Sprintf("Node-to-node overlay verified: %s → %d node(s) reachable on port %d",
			checkerNode, len(targetIPs), nodeToNodeTestPort))
		ok := true
		state.NodeToNodeOK = &ok
	} else {
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
}

// waitForDaemonSetDesiredCount polls until the DaemonSet controller has
// reconciled the object and published a scheduling target. The Create response
// always has a zeroed status, so reading DesiredNumberScheduled from it yields
// 0 on every real cluster.
func waitForDaemonSetDesiredCount(
	ctx context.Context, client kubernetes.Interface, ns, name string, timeout time.Duration,
) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for {
		ds, err := client.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err != nil:
			// Retry inside the deadline rather than aborting. client-go defaults
			// to 5 QPS and this run issues ~22 namespaced LISTs, so a single 429
			// early in the window would otherwise fail a critical check with
			// most of its budget unspent. Only a permission error is terminal.
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return 0, err
			}
			lastStatus = err.Error()
		case ds.Status.ObservedGeneration >= ds.Generation && ds.Status.DesiredNumberScheduled > 0:
			return int(ds.Status.DesiredNumberScheduled), nil
		default:
			lastStatus = fmt.Sprintf("desired=%d, observedGeneration=%d, generation=%d",
				ds.Status.DesiredNumberScheduled, ds.Status.ObservedGeneration, ds.Generation)
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timed out waiting for DaemonSet status (%s)", lastStatus)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitForDaemonSetPods waits for the DaemonSet's pods to come up. It returns as
// soon as wantCount pods are Running, and on timeout still returns whatever it
// has if that covers minNodes distinct nodes.
//
// The partial return matters because wantCount comes from
// DesiredNumberScheduled, which includes NotReady and cordoned nodes: the
// DaemonSet controller auto-tolerates those taints. Requiring every pod would
// fail this critical check on one NotReady node, contradicting the same run's
// non-blocking "Worker Nodes: N NotReady" policy. Two nodes are enough to
// prove the overlay carries cross-node traffic.
func waitForDaemonSetPods(
	ctx context.Context, client kubernetes.Interface, ns, selector string,
	wantCount, minNodes int, timeout time.Duration,
) ([]corev1.Pod, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			// Same reasoning as waitForDaemonSetDesiredCount: retry transient
			// errors inside the deadline instead of failing the check outright.
			if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
				return nil, err
			}
			lastErr = err
		}
		var running []corev1.Pod
		if err == nil {
			lastErr = nil
			for i := range pods.Items {
				if pods.Items[i].Status.Phase == corev1.PodRunning && pods.Items[i].Status.PodIP != "" {
					running = append(running, pods.Items[i])
				}
			}
			if len(running) >= wantCount {
				return running, nil
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("listing DaemonSet pods: %w", lastErr)
			}
			if distinctNodeCount(running) >= minNodes {
				return running, nil
			}
			return nil, fmt.Errorf("timed out waiting for %d Running pods (got %d on %d node(s))",
				wantCount, len(running), distinctNodeCount(running))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// distinctNodeCount counts how many different nodes a pod set covers.
func distinctNodeCount(pods []corev1.Pod) int {
	nodes := make(map[string]struct{}, len(pods))
	for i := range pods {
		if n := pods[i].Spec.NodeName; n != "" {
			nodes[n] = struct{}{}
		}
	}
	return len(nodes)
}

// nodeToNodeTolerations mirrors the validator CronJob's own tolerations. The
// DaemonSet controller auto-tolerates the not-ready and unschedulable taints
// but not the control-plane one, so without these a dedicated control plane
// reports DesiredNumberScheduled=0 and the overlay is never probed at all.
func nodeToNodeTolerations() []corev1.Toleration {
	return []corev1.Toleration{
		{Key: "node-role.kubernetes.io/control-plane", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "node-role.kubernetes.io/master", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
	}
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

func buildNodeToNodeDaemonSet(name, namespace string, labels map[string]string, image string) *appsv1.DaemonSet {
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
					Containers: []corev1.Container{{
						Name:            "server",
						Image:           image,
						Command:         []string{"sh", "-c", fmt.Sprintf("while true; do nc -l -p %d; done", nodeToNodeTestPort)},
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
	var cmds []string
	for _, ip := range targetIPs {
		cmds = append(cmds, fmt.Sprintf("nc -z -w 5 %s %d || exit 1", ip, nodeToNodeTestPort))
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
				Command:         []string{"sh", "-c", strings.Join(cmds, " && ")},
				Resources:       enforcementResources(),
				SecurityContext: nodeToNodeSecurityContext(),
			}},
		},
	}
}

// controlPlaneNamespaces is the set of namespaces scanned by Tier-1 and
// Tier-2 HA checks on the control-plane cluster.
// controlPlaneNamespaces lists the namespaces the self-managed stack deploys
// into, per deploy/stacks/self-managed/helmfile.d. Namespaces that are absent
// are skipped silently (a LIST against a missing namespace returns an empty
// 200), so listing one that a given install does not use is harmless.
//
// The OpenBao namespace is overridable via NVCF_OPENBAO_NAMESPACE, so its
// configured value is appended at runtime by controlPlaneNamespaceSet.
var controlPlaneNamespaces = []string{
	"nvcf", "sis", "api-keys", "ess", "nvcf-ui",
	"nats-system", "vault-system", "cassandra-system",
	"cert-manager", "envoy-gateway-system",
}

// openBaoNamespaceEnv mirrors the nvcf-cli override so a cluster that relocates
// OpenBao does not silently drop its StatefulSet from the Tier-2 check.
const openBaoNamespaceEnv = "NVCF_OPENBAO_NAMESPACE"

// controlPlaneNamespaceSet returns controlPlaneNamespaces plus any
// runtime-configured OpenBao and Envoy Gateway namespaces, de-duplicated.
func controlPlaneNamespaceSet() []string {
	out := append([]string(nil), controlPlaneNamespaces...)
	for _, env := range []string{openBaoNamespaceEnv, envoyGatewayNamespaceEnv} {
		extra := strings.TrimSpace(os.Getenv(env))
		if extra == "" {
			continue
		}
		seen := false
		for _, ns := range out {
			if ns == extra {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, extra)
		}
	}
	return out
}

// envoyGatewayNamespaceName is where the Envoy Gateway controller and its
// provisioned proxy Services live.
func envoyGatewayNamespaceName() string {
	if ns := strings.TrimSpace(os.Getenv(envoyGatewayNamespaceEnv)); ns != "" {
		return ns
	}
	return envoyGatewayNamespace
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

// checkTier1Deployments verifies that every Deployment in the control-plane
// namespaces has readyReplicas >= spec.replicas. Any under-replicated Deployment
// means HA headroom is gone and a second failure causes a full outage.
//
// The check is generic; no hardcoded Deployment names. New services added to
// those namespaces are automatically covered.
//
// Critical: under-replication means a single additional failure causes a full
// service outage.
func checkTier1Deployments(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Tier-1 Deployment Readiness")

	var underReplicated []string
	var scaledToZero []string
	checkedCount := 0
	deniedCount := 0
	rollingCount := 0
	// rollingUnderReplicated counts mid-rollout Deployments that are also
	// below their target, which bounds the skip: a paused or sentinel-deadline
	// Deployment can stay "rolling" forever, but if it is still serving its
	// full replica count there is nothing to report.
	rollingUnderReplicated := 0

	for _, ns := range controlPlaneNamespaceSet() {
		deploys, err := client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			// A 403 means we could not observe the namespace, not that it is
			// healthy. Track it separately so it cannot reach the trivial-pass
			// exit below. A LIST against a missing namespace returns an empty
			// 200, so IsNotFound is not a case here.
			if apierrors.IsForbidden(err) {
				deniedCount++
				continue
			}
			// Same shape as the 403 branch: keep going. Returning here throws
			// away the under-replicated Deployments already collected from
			// earlier namespaces and publishes the tier as unknown, even
			// though a fully-down service was observed.
			printWarning(log, fmt.Sprintf("Could not list Deployments in %s: %v", ns, err))
			state.Warnings = append(state.Warnings,
				fmt.Sprintf("Tier-1 Deployments: status unknown (listing failed in %s)", ns))
			deniedCount++
			continue
		}
		for i := range deploys.Items {
			d := &deploys.Items[i]
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			// A Deployment scaled to zero satisfies "ReadyReplicas >= want"
			// with nothing running at all, so counting it as healthy lets a
			// maintenance scale-down or a replicaCount:0 values error publish
			// the critical row as All Ready. Report it instead of counting it.
			if want == 0 {
				scaledToZero = append(scaledToZero, ns+"/"+d.Name)
				continue
			}
			// A rollout transiently drops readyReplicas below spec.replicas on
			// a healthy cluster, so skip those. But UpdatedReplicas < want is
			// not self-limiting: a bad image wedges there permanently with
			// ObservedGeneration == Generation. ProgressDeadlineExceeded is the
			// signal that separates "in flight" from "stuck", so a stalled
			// rollout falls through to the under-replicated check below.
			rollingOut := d.Status.ObservedGeneration < d.Generation ||
				d.Status.UpdatedReplicas < want
			if rollingOut && !deploymentRolloutStalled(d) {
				msg := fmt.Sprintf("%s/%s: rollout in progress (updated: %d/%d); re-run check after rollout completes",
					ns, d.Name, d.Status.UpdatedReplicas, want)
				printWarning(log, msg)
				state.Warnings = append(state.Warnings, "Tier-1 Deployments: "+msg)
				rollingCount++
				// ProgressDeadlineExceeded is never set for a paused rollout,
				// for progressDeadlineSeconds=2147483647, or for a wedged
				// controller, so rollingOut alone is not self-limiting. Only a
				// skipped Deployment that is ALSO below its ready target can
				// hide a problem, so only those make the tier unknown.
				if d.Status.ReadyReplicas < want {
					rollingUnderReplicated++
				}
				continue
			}
			checkedCount++
			if d.Status.ReadyReplicas < want {
				underReplicated = append(underReplicated,
					fmt.Sprintf("%s/%s (ready: %d, want: %d)", ns, d.Name, d.Status.ReadyReplicas, want))
			}
		}
	}

	if checkedCount == 0 {
		if deniedCount > 0 {
			// Leave nil: every namespace was denied, so nothing was observed.
			printWarning(log, fmt.Sprintf("Deployments not readable in %d control-plane namespace(s)", deniedCount))
			state.Warnings = append(state.Warnings,
				"Tier-1 Deployments: status unknown (RBAC denied Deployment list in all control-plane namespaces)")
			return
		}
		if rollingCount > 0 {
			// Deployments exist but every one is mid-rollout, so readiness
			// cannot be assessed yet. Reporting "pre-install" here would be wrong.
			printWarning(log, fmt.Sprintf("All %d Deployment(s) are mid-rollout; readiness not assessed", rollingCount))
			return
		}
		if len(scaledToZero) > 0 {
			// Every Deployment present is scaled to zero: the namespaces are
			// populated but nothing is running, which is not a pass.
			printError(log, fmt.Sprintf("All %d Deployment(s) are scaled to zero replicas: %s",
				len(scaledToZero), strings.Join(scaledToZero, ", ")))
			ok := false
			state.Tier1DeploymentsOK = &ok
			return
		}
		printInfo(log, "  No Deployments found in control-plane namespaces (pre-install state)")
		ok := true
		state.Tier1DeploymentsOK = &ok
		return
	}

	// Surfaced as a warning rather than a failure: scaling a component down is
	// a legitimate operator action, but it must not be invisible on a row that
	// claims every Deployment is ready.
	if len(scaledToZero) > 0 {
		msg := fmt.Sprintf("%d Deployment(s) scaled to zero replicas: %s",
			len(scaledToZero), strings.Join(scaledToZero, ", "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Tier-1 Deployments: "+msg)
	}

	if len(underReplicated) > 0 {
		printError(log, fmt.Sprintf("Under-replicated Deployments (%d):", len(underReplicated)))
		for _, name := range underReplicated {
			printInfo(log, "  "+name)
		}
		state.Recommendations = append(state.Recommendations,
			"Check for crashed, evicted, or unschedulable pods in the listed namespaces. "+
				"If a service is intentionally single-replica, raise its replicaCount in the "+
				"self-managed stack values to keep HA headroom.")
		ok := false
		state.Tier1DeploymentsOK = &ok
		return
	}

	if deniedCount > 0 {
		// Some namespaces were never observed, so "all ready" is not a claim we
		// can make even though every Deployment we could see passed.
		printWarning(log, fmt.Sprintf("%d Deployment(s) ready, but %d namespace(s) were not readable",
			checkedCount, deniedCount))
		state.Warnings = append(state.Warnings,
			"Tier-1 Deployments: status unknown (RBAC denied Deployment list in one or more control-plane namespaces)")
		return
	}

	if rollingUnderReplicated > 0 {
		// Only skipped Deployments that are also below their ready target make
		// the tier unknown. A paused or sentinel-deadline Deployment serving
		// its full replica count is skipped but hides nothing, so it must not
		// pin this critical row to UNKNOWN indefinitely.
		printWarning(log, fmt.Sprintf("%d Deployment(s) ready, but %d are mid-rollout and under-replicated; assessment is partial",
			checkedCount, rollingUnderReplicated))
		state.Warnings = append(state.Warnings,
			"Tier-1 Deployments: status unknown (one or more Deployments are mid-rollout and below their replica target)")
		return
	}

	if rollingCount > 0 {
		printWarning(log, fmt.Sprintf("%d Deployment(s) ready, %d mid-rollout but still at their replica target",
			checkedCount, rollingCount))
	}

	printSuccess(log, fmt.Sprintf("All %d assessed Deployment(s) in control-plane namespaces are fully ready", checkedCount))
	ok := true
	state.Tier1DeploymentsOK = &ok
}

// checkTier2StatefulSets verifies quorum membership and node placement for
// Tier-2 stateful components (NATS JetStream, OpenBao Raft, Cassandra).
// Any StatefulSet with an odd spec.replicas of 3 or more is treated as a quorum
// component and checked for:
//  1. readyReplicas == spec.replicas
//  2. all pods on distinct nodes
//
// StatefulSets mid-rolling-update are warned about, not failed: they roll one
// pod at a time, so a below-target ready count is the steady state for the
// duration of any upgrade.
//
// The check is generic; no hardcoded StatefulSet names.
//
// Critical: broken quorum or co-located peers leave the stack one failure
// away from a total control-plane outage.
func checkTier2StatefulSets(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Tier-2 StatefulSet Quorum and Placement")

	const minQuorumSize = int32(3)
	var failures []string
	var skippedParity []string
	checkedCount := 0
	deniedCount := 0
	rollingCount := 0
	// rollingUnderReplicated bounds the rollout skip, as in checkTier1Deployments.
	rollingUnderReplicated := 0
	// placementUnknown counts StatefulSets whose pods could not be listed, so
	// an unreadable namespace cannot masquerade as a clean placement result.
	placementUnknown := 0

	for _, ns := range controlPlaneNamespaceSet() {
		stsList, err := client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			// See checkTier1Deployments: a 403 must not reach the trivial-pass
			// exit. This fires today, as the validator ClusterRole grants
			// deployments and daemonsets but not statefulsets.
			if apierrors.IsForbidden(err) {
				deniedCount++
				continue
			}
			// See checkTier1Deployments: continue rather than return, so
			// quorum failures already observed are not discarded.
			printWarning(log, fmt.Sprintf("Could not list StatefulSets in %s: %v", ns, err))
			state.Warnings = append(state.Warnings,
				fmt.Sprintf("Tier-2 StatefulSets: status unknown (listing failed in %s)", ns))
			deniedCount++
			continue
		}

		for i := range stsList.Items {
			sts := &stsList.Items[i]
			// Any odd replica count of 3 or more is a quorum member. Requiring
			// exactly 3 silently drops a Cassandra scaled to 5 from the check
			// rather than failing it.
			if sts.Spec.Replicas == nil {
				continue
			}
			want := *sts.Spec.Replicas
			if want < minQuorumSize {
				continue
			}
			if want%2 == 0 {
				// Even replica counts are not a quorum shape this check can
				// reason about, but they are not nothing either: a 4-replica
				// Cassandra with RF=3 can have lost quorum. Record it so an
				// all-even cluster cannot reach the trivial-pass exit below
				// having examined no StatefulSet at all.
				skippedParity = append(skippedParity, fmt.Sprintf("%s/%s (replicas=%d)", ns, sts.Name, want))
				continue
			}

			// StatefulSets roll one pod at a time, so readyReplicas == want-1
			// is the steady state for the whole duration of any image bump,
			// PVC resize, or node drain. Warn rather than fail, unless the
			// controller has not even observed the current generation.
			// CurrentRevision only advances when a RollingUpdate completes, so
			// a revision mismatch is permanent for updateStrategy OnDelete
			// (which this repo's OpenBao uses), for a non-zero
			// rollingUpdate.partition, and for a wedged rollout. There is no
			// StatefulSet equivalent of ProgressDeadlineExceeded, so bound the
			// tolerance by readiness instead: a StatefulSet at its full ready
			// count is not hiding anything, and one below it is reported.
			if sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision != sts.Status.UpdateRevision {
				msg := fmt.Sprintf("%s/%s: revision mismatch (ready: %d/%d)",
					ns, sts.Name, sts.Status.ReadyReplicas, want)
				printWarning(log, msg)
				state.Warnings = append(state.Warnings, "Tier-2 StatefulSets: "+msg)
				rollingCount++

				switch {
				case sts.Status.ReadyReplicas >= want:
					// Full ready count despite the mismatch: nothing is hidden,
					// so assess it normally and let the placement scan run.
					// This is the steady state for OnDelete and for a non-zero
					// rollingUpdate.partition, where the mismatch never clears.
				case sts.Status.ReadyReplicas == want-1:
					// Exactly one pod down is what rolling one at a time looks
					// like, so tolerate it but do not claim the tier is clean.
					rollingUnderReplicated++
					continue
				default:
					// More than one peer down is beyond what a rolling update
					// explains, mismatch or not.
					failures = append(failures,
						fmt.Sprintf("%s/%s: readyReplicas=%d (need %d, revision mismatch)",
							ns, sts.Name, sts.Status.ReadyReplicas, want))
					checkedCount++
					continue
				}
			}
			checkedCount++

			if sts.Status.ReadyReplicas < want {
				failures = append(failures,
					fmt.Sprintf("%s/%s: readyReplicas=%d (need %d)",
						ns, sts.Name, sts.Status.ReadyReplicas, want))
				continue
			}

			selector := metav1.FormatLabelSelector(sts.Spec.Selector)
			pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				// Not a placement failure: we could not look. Recording it in
				// failures would report a broken quorum for an RBAC gap on
				// pods, while the identical gap on statefulsets above is
				// correctly reported as unknown.
				msg := fmt.Sprintf("%s/%s: could not list pods for placement check: %v", ns, sts.Name, err)
				printWarning(log, msg)
				state.Warnings = append(state.Warnings, "Tier-2 StatefulSets: "+msg)
				placementUnknown++
				continue
			}

			// Count only Ready pods owned by this StatefulSet. Phase stays
			// Running through CrashLoopBackOff, and a surplus pod left over
			// from a rollout would otherwise be reported as a co-location.
			nodeOwner := make(map[string]string)
			for j := range pods.Items {
				p := &pods.Items[j]
				if !metav1.IsControlledBy(p, sts) || !isPodReady(p) {
					continue
				}
				if first, dup := nodeOwner[p.Spec.NodeName]; dup {
					failures = append(failures,
						fmt.Sprintf("%s/%s: pods %s and %s are co-located on node %s",
							ns, sts.Name, first, p.Name, p.Spec.NodeName))
				} else {
					nodeOwner[p.Spec.NodeName] = p.Name
				}
			}
		}
	}

	if checkedCount == 0 {
		if deniedCount > 0 {
			printWarning(log, fmt.Sprintf("StatefulSets not readable in %d control-plane namespace(s)", deniedCount))
			state.Warnings = append(state.Warnings,
				"Tier-2 StatefulSets: status unknown (RBAC denied StatefulSet list in all control-plane namespaces)")
			return
		}
		if rollingCount > 0 {
			printWarning(log, fmt.Sprintf("All %d quorum StatefulSet(s) are mid-rollout; quorum not assessed", rollingCount))
			return
		}
		if len(skippedParity) > 0 {
			// StatefulSets exist in the quorum namespaces but none has an odd
			// replica count, so nothing was examined. Claiming "Quorum and
			// Placement OK" here would certify a ring this check never looked at.
			printWarning(log, fmt.Sprintf("No odd-replica quorum StatefulSets; %d even-replica StatefulSet(s) not assessed: %s",
				len(skippedParity), strings.Join(skippedParity, ", ")))
			state.Warnings = append(state.Warnings,
				"Tier-2 StatefulSets: status unknown (only even-replica StatefulSets present: "+
					strings.Join(skippedParity, ", ")+")")
			return
		}
		if placementUnknown > 0 {
			printWarning(log, fmt.Sprintf("Placement not assessed for %d StatefulSet(s); pods were not readable", placementUnknown))
			return
		}
		printInfo(log, "  No quorum StatefulSets (odd spec.replicas >= 3) found (pre-install or non-HA install)")
		ok := true
		state.Tier2StatefulSetsOK = &ok
		return
	}

	if len(failures) > 0 {
		printError(log, fmt.Sprintf("Tier-2 quorum/placement findings (%d):", len(failures)))
		for _, f := range failures {
			printInfo(log, "  "+f)
		}
		state.Recommendations = append(state.Recommendations,
			"Ensure each Tier-2 StatefulSet (NATS, OpenBao, Cassandra) has all spec.replicas pods Ready "+
				"and spread across distinct nodes.")
		ok := false
		state.Tier2StatefulSetsOK = &ok
		return
	}

	if deniedCount > 0 {
		printWarning(log, fmt.Sprintf("%d quorum StatefulSet(s) healthy, but %d namespace(s) were not readable",
			checkedCount, deniedCount))
		state.Warnings = append(state.Warnings,
			"Tier-2 StatefulSets: status unknown (RBAC denied StatefulSet list in one or more control-plane namespaces)")
		return
	}

	if placementUnknown > 0 {
		printWarning(log, fmt.Sprintf("%d quorum StatefulSet(s) healthy, but placement was not assessed for %d",
			checkedCount, placementUnknown))
		state.Warnings = append(state.Warnings,
			"Tier-2 StatefulSets: status unknown (pod placement could not be read for one or more StatefulSets)")
		return
	}

	if rollingUnderReplicated > 0 {
		printWarning(log, fmt.Sprintf("%d quorum StatefulSet(s) healthy, but %d are mid-rollout and below target; assessment is partial",
			checkedCount, rollingUnderReplicated))
		state.Warnings = append(state.Warnings,
			"Tier-2 StatefulSets: status unknown (one or more StatefulSets are mid-rollout and below their replica target)")
		return
	}

	if len(skippedParity) > 0 {
		msg := fmt.Sprintf("%d even-replica StatefulSet(s) not assessed: %s",
			len(skippedParity), strings.Join(skippedParity, ", "))
		printWarning(log, msg)
		state.Warnings = append(state.Warnings, "Tier-2 StatefulSets: "+msg)
	}

	if rollingCount > 0 {
		printWarning(log, fmt.Sprintf("%d quorum StatefulSet(s) healthy, %d mid-rollout but at their replica target",
			checkedCount, rollingCount))
	}

	printSuccess(log, fmt.Sprintf("All %d quorum StatefulSet(s) Ready on distinct nodes", checkedCount))
	ok := true
	state.Tier2StatefulSetsOK = &ok
}

// checkConfigurableReachability probes user-defined endpoints loaded from the
// cluster-validator ConfigMap.
func checkConfigurableReachability(state *ValidationState, cfg *ReachabilityConfig) {
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

		// Surface the implicit https→tcp+tls fallback so the operator
		// can see that the probe protocol differs from what they wrote.
		if ep.Protocol == protocolHTTPS && ep.URL == "" && target.Protocol == protocolTCPTLS {
			printInfo(log, fmt.Sprintf(
				"  %s: https without 'url' — probing %s via tcp+tls", ep.Name, display))
		}

		// Pre-flight: surface a clear diagnostic when the endpoint config
		// is missing fields required by its protocol, instead of letting
		// it fall through to a silent "Not Reachable" that's
		// indistinguishable from a real connectivity failure.
		if reason := unprobableReason(target); reason != "" {
			allOK = false
			state.EndpointResults[ep.Name] = EndpointResult{Reachable: false, Critical: ep.Critical}
			msg := fmt.Sprintf("  %s: %s — %s (treated as unreachable)", ep.Name, display, reason)
			if ep.Critical {
				allCriticalOK = false
				printError(log, msg)
			} else {
				printWarning(log, msg)
			}
			continue
		}

		if TestEndpoint(target) {
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
		// Don't assume egress is the cause — DNS resolution failures (typo
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
	// host is set, and tcp+tls is the equivalent probe — the same
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
