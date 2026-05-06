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
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	return nil
}

// checkControlPlaneHealth checks API server readiness, critical kube-system
// pods, and node status.
func checkControlPlaneHealth(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	log := state.Log
	printHeader(log, "Kubernetes Control Plane Health")
	allHealthy := true

	if _, err := client.Discovery().ServerVersion(); err == nil {
		printSuccess(log, "API Server is ready")
	} else {
		printError(log, "API Server is not ready")
		allHealthy = false
	}
	log.Info("")
	log.Info("Control Plane Pods (kube-system):")
	criticalPods := []string{
		"kube-apiserver",
		"kube-controller-manager",
		"kube-scheduler",
		"etcd",
		"coredns",
		"kube-proxy",
	}

	pods, err := client.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		printError(log, fmt.Sprintf("Failed to list kube-system pods: %v", err))
		allHealthy = false
	} else {
		for _, prefix := range criticalPods {
			count := 0
			for i := range pods.Items {
				p := &pods.Items[i]
				if strings.HasPrefix(p.Name, prefix) && p.Status.Phase == corev1.PodRunning {
					count++
				}
			}
			if count > 0 {
				printSuccess(log, fmt.Sprintf("  %s: %d instance(s) running", prefix, count))
			} else {
				printWarning(log, fmt.Sprintf("  %s: Not found or not running", prefix))
				allHealthy = false
			}
		}
	}
	log.Info("")
	log.Info("Node Status:")
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		printError(log, fmt.Sprintf("Failed to list nodes: %v", err))
		allHealthy = false
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
			printWarning(log, fmt.Sprintf("  NotReady nodes: %d", notReady))
			allHealthy = false
		}
	}
	log.Info("")
	if allHealthy {
		printSuccess(log, "Control plane is healthy")
	} else {
		printWarning(log, "Some control plane components may need attention")
		state.ControlPlaneHealthy = false
		state.Recommendations = append(state.Recommendations,
			"Fix control plane issues: Check node status and kube-system pods")
	}
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
		printError(log, "SMB CSI Driver is NOT installed")
		printInfo(log, fmt.Sprintf("SMB CSI Driver v%s+ is required for persistent storage", requiredVersion))
		printInfo(log, "To install SMB CSI Driver:")
		log.Info("# Using Helm:")
		log.Info("helm repo add csi-driver-smb https://raw.githubusercontent.com/kubernetes-csi/csi-driver-smb/master/charts")
		log.Info("helm install csi-driver-smb csi-driver-smb/csi-driver-smb \\")
		log.Info("  --namespace kube-system \\")
		log.Infof("  --version v%s", requiredVersion)
		printInfo(log, "For more information: https://github.com/kubernetes-csi/csi-driver-smb")
		state.Recommendations = append(state.Recommendations,
			fmt.Sprintf("Install SMB CSI Driver v%s or higher", requiredVersion))
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
			"Install GPU Operator using the command above")
	} else {
		printSuccess(log, "GPU Operator is installed")
		state.GPUOperatorInstalled = true
	}
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

	for _, ep := range cfg.Endpoints {
		target := toEndpoint(ep)
		display := target.DisplayAddr()

		if ep.Critical {
			hasCritical = true
		}

		if TestEndpoint(target) {
			printSuccess(log, fmt.Sprintf("  %s: %s - Reachable", ep.Name, display))
		} else {
			allOK = false
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
		printError(log, "Some critical endpoints are not reachable")
		state.Recommendations = append(state.Recommendations,
			"Ensure network egress is allowed to all critical endpoints listed above")
	} else {
		printWarning(log, "Some endpoints are not reachable (non-critical)")
		state.Warnings = append(state.Warnings,
			"Reachability: Some endpoints are not reachable")
	}
}

func toEndpoint(ep ReachabilityEndpoint) Endpoint {
	return Endpoint{
		URL:      ep.URL,
		Host:     ep.Host,
		Port:     ep.Port,
		Protocol: ep.Protocol,
	}
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
