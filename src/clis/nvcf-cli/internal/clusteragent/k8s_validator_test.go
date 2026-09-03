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

package clusteragent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func newFakeValidator(httpCli *http.Client, dynObjs, k8sObjs []runtime.Object) *k8sValidator {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		nvcfBackendGVR: "NVCFBackendList",
		icmsRequestGVR: "ICMSRequestList",
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, dynObjs...)
	cs := k8sfake.NewSimpleClientset(k8sObjs...)
	return &k8sValidator{cs: cs, dc: dc, httpCli: httpCli}
}

// validatorBackend builds an NVCFBackend CR with the status fields the checks
// read. Zero gpuCapacity and empty agentStatus omit those sections.
func validatorBackend(systemNS, agentStatus string, gpuCapacity, gpuAllocated int64) *unstructured.Unstructured {
	cc := map[string]interface{}{
		"clusterId":       testClusterID,
		"clusterName":     testCluster,
		"systemNamespace": systemNS,
	}
	status := map[string]interface{}{}
	if agentStatus != "" {
		status["agentStatus"] = agentStatus
	}
	if gpuCapacity > 0 || gpuAllocated > 0 {
		status["gpuUsage"] = map[string]interface{}{
			"A100": map[string]interface{}{
				"capacity":  gpuCapacity,
				"available": gpuCapacity - gpuAllocated,
				"allocated": gpuAllocated,
			},
		}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvcf.nvidia.io/v1",
		"kind":       "NVCFBackend",
		"metadata":   map[string]interface{}{"namespace": testBackendNS, "name": "backend"},
		"spec":       map[string]interface{}{"clusterConfig": cc},
		"status":     status,
	}}
}

func validatorNVCADeploy(systemNS string, desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: nvcaDeploymentName, Namespace: systemNS},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func gpuNode(name string, gpus int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				gpuResourceName: *resource.NewQuantity(gpus, resource.DecimalSI),
			},
		},
	}
}

func dockerSecret(ns, name string, valid bool) *corev1.Secret {
	data := `{"auths":{"nvcr.io":{"auth":"dXNlcjpwYXNz"}}}`
	if !valid {
		data = `{"auths":{}}`
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(data)},
	}
}

func tlsSecret(t *testing.T, ns, name string, notAfter time.Time) *corev1.Secret {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{corev1.TLSCertKey: certPEM},
	}
}

// icmsWithInstances builds an ICMSRequest with explicit instance statuses, used
// by the deployment checks where the inspector's fixed Running instance is not
// expressive enough.
func icmsWithInstances(ns, name, fid, vid, requestStatus string, instanceStatuses map[string]string) *unstructured.Unstructured {
	insts := map[string]interface{}{}
	for id, st := range instanceStatuses {
		insts[id] = map[string]interface{}{"id": id, "status": st}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "nvca.nvcf.nvidia.io/v2beta1",
		"kind":       "ICMSRequest",
		"metadata":   map[string]interface{}{"namespace": ns, "name": name},
		"spec":       map[string]interface{}{"functionDetails": map[string]interface{}{"functionId": fid, "functionVersionId": vid}},
		"status":     map[string]interface{}{"requestStatus": requestStatus, "instances": insts},
	}}
}

// fakePod builds a minimal Pod for the fake typed clientset with a given
// PodReady condition and no container statuses (a clean-ready or
// not-yet-scheduled case).
func fakePod(ns, name string, ready corev1.ConditionStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready}},
		},
	}
}

// fakeTerminatedPod builds a not-ready Pod whose container is currently
// terminated with a non-zero exit code, matching a container that just
// crashed (Pod phase stays Running under restartPolicy: Always, only the
// container state reflects the crash).
func fakeTerminatedPod(ns, name, containerName string, exitCode int32, reason string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  containerName,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exitCode, Reason: reason}},
			}},
		},
	}
}

// fakeCrashLoopingPod builds a not-ready Pod whose container is currently
// cycling (Waiting) but has restarted past the failure threshold, with its
// last crash recorded in LastTerminationState -- the case where the
// container is never caught mid-Terminated by a point-in-time check.
func fakeCrashLoopingPod(ns, name, containerName string, restartCount, lastExitCode int32, lastReason string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         containerName,
				RestartCount: restartCount,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{ExitCode: lastExitCode, Reason: lastReason},
				},
			}},
		},
	}
}

// checkByName returns the result for one check, failing the test if it is absent.
func checkByName(t *testing.T, checks []CheckResult, name string) CheckResult {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %+v", name, checks)
	return CheckResult{}
}

// --- Validate ---

func TestValidateAllChecksPass(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5)},
		[]runtime.Object{
			validatorNVCADeploy(testSystemNS, 1, 1),
			gpuNode("node-1", 8),
			dockerSecret(testSystemNS, "ngc-pull", true),
			tlsSecret(t, testSystemNS, "nvca-tls", nowFunc().Add(90*24*time.Hour)),
		},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if res.ClusterID != testClusterID || res.ClusterName != testCluster {
		t.Errorf("cluster identity = %q/%q, want %q/%q", res.ClusterID, res.ClusterName, testClusterID, testCluster)
	}
	if len(res.Checks) != len(AllClusterChecks) {
		t.Fatalf("ran %d checks, want %d", len(res.Checks), len(AllClusterChecks))
	}
	if res.HasFailure() {
		t.Fatalf("expected no failures, got %+v", res.Checks)
	}
	for _, name := range []string{CheckNVCAReachable, CheckGPUCapacity, CheckNATSHealth, CheckPullSecret, CheckTLSCert} {
		if got := checkByName(t, res.Checks, name); got.Status != CheckPassed {
			t.Errorf("%s = %s (%s), want PASS", name, got.Status, got.Message)
		}
	}
}

func TestValidateNVCAReachableFailsWhenNotReady(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5)},
		[]runtime.Object{validatorNVCADeploy(testSystemNS, 2, 0)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckNVCAReachable}})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	got := checkByName(t, res.Checks, CheckNVCAReachable)
	if got.Status != CheckFailed {
		t.Errorf("nvca-reachable = %s, want FAIL (%s)", got.Status, got.Message)
	}
}

func TestValidateGPUCapacityMismatchWarns(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 0)},
		[]runtime.Object{gpuNode("node-1", 4)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckGPUCapacity}})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	got := checkByName(t, res.Checks, CheckGPUCapacity)
	if got.Status != CheckWarning {
		t.Errorf("gpu-capacity = %s, want WARN (%s)", got.Status, got.Message)
	}
}

func TestValidatePullSecretFailsOnMalformed(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 0, 0)},
		[]runtime.Object{dockerSecret(testSystemNS, "bad-pull", false)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckPullSecret}})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	got := checkByName(t, res.Checks, CheckPullSecret)
	if got.Status != CheckFailed {
		t.Errorf("pull-secret = %s, want FAIL (%s)", got.Status, got.Message)
	}
}

func TestValidateTLSCertExpiryWindows(t *testing.T) {
	t.Run("warns within window", func(t *testing.T) {
		v := newFakeValidator(nil,
			[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 0, 0)},
			[]runtime.Object{tlsSecret(t, testSystemNS, "soon", nowFunc().Add(10*24*time.Hour))},
		)
		res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckTLSCert}})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if got := checkByName(t, res.Checks, CheckTLSCert); got.Status != CheckWarning {
			t.Errorf("tls-cert = %s, want WARN (%s)", got.Status, got.Message)
		}
	})

	t.Run("fails when expired", func(t *testing.T) {
		v := newFakeValidator(nil,
			[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 0, 0)},
			[]runtime.Object{tlsSecret(t, testSystemNS, "expired", nowFunc().Add(-time.Hour))},
		)
		res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckTLSCert}})
		if err != nil {
			t.Fatalf("Validate returned error: %v", err)
		}
		if got := checkByName(t, res.Checks, CheckTLSCert); got.Status != CheckFailed {
			t.Errorf("tls-cert = %s, want FAIL (%s)", got.Status, got.Message)
		}
	})
}

func TestValidateCheckFilterRunsOnlyNamed(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5)},
		[]runtime.Object{gpuNode("node-1", 8)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckGPUCapacity}})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if len(res.Checks) != 1 || res.Checks[0].Name != CheckGPUCapacity {
		t.Fatalf("ran %+v, want only gpu-capacity", res.Checks)
	}
}

func TestValidateFailFastStopsAfterFirstFailure(t *testing.T) {
	// No NVCA deployment -> nvca-reachable (the first check) fails; fail-fast must
	// stop before running the rest.
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5)},
		[]runtime.Object{gpuNode("node-1", 8)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, FailFast: true})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if len(res.Checks) != 1 {
		t.Fatalf("fail-fast ran %d checks, want 1: %+v", len(res.Checks), res.Checks)
	}
	if res.Checks[0].Name != CheckNVCAReachable || res.Checks[0].Status != CheckFailed {
		t.Errorf("first check = %+v, want nvca-reachable FAIL", res.Checks[0])
	}
}

func TestValidateErrorsWithoutBackend(t *testing.T) {
	v := newFakeValidator(nil, nil, nil)
	if _, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS}); err == nil {
		t.Fatal("expected error when no NVCFBackend exists")
	}
}

func TestValidateNVCAReachableHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	v := newFakeValidator(srv.Client(),
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 0, 0)},
		[]runtime.Object{validatorNVCADeploy(testSystemNS, 1, 1)},
	)

	res, err := v.Validate(context.Background(), ValidateOptions{BackendNS: testBackendNS, CheckNames: []string{CheckNVCAReachable}, NVCAURL: srv.URL})
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if got := checkByName(t, res.Checks, CheckNVCAReachable); got.Status != CheckPassed {
		t.Errorf("nvca-reachable = %s, want PASS (%s)", got.Status, got.Message)
	}
}

// --- ValidateDeployment ---

func TestValidateDeploymentPodReadinessFails(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{
			validatorBackend(testSystemNS, agentStatusHealthy, 8, 5),
			icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusCompleted, map[string]string{"inst-a": "Error"}),
		},
		nil,
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "v1", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	if out.FunctionID != "fn-1" || out.FunctionVersionID != "v1" {
		t.Errorf("identity = %q/%q, want fn-1/v1", out.FunctionID, out.FunctionVersionID)
	}
	if got := checkByName(t, out.Checks, "pod-readiness"); got.Status != CheckFailed {
		t.Errorf("pod-readiness = %s, want FAIL (%s)", got.Status, got.Message)
	}
}

// TestValidateDeploymentPodReadinessFailsOnCrashedContainerDespiteCleanCRStatus
// is the direct regression test for the bug this fix addresses: the
// ICMSRequest CR's mirrored status string looks healthy (NVCA hasn't
// reconciled the crash into it yet), but the real Pod's container has
// terminated with a non-zero exit code. pod-readiness must fail from the
// live Pod, not the stale CR string.
func TestValidateDeploymentPodReadinessFailsOnCrashedContainerDespiteCleanCRStatus(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{
			validatorBackend(testSystemNS, agentStatusHealthy, 8, 5),
			icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusInProgress, map[string]string{"inst-a": "starting"}),
		},
		[]runtime.Object{fakeTerminatedPod(testRequestsNS, "inst-a", "inference", 127, "Error")},
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "v1", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	got := checkByName(t, out.Checks, "pod-readiness")
	if got.Status != CheckFailed {
		t.Errorf("pod-readiness = %s, want FAIL (%s)", got.Status, got.Message)
	}
	if !strings.Contains(got.Message, "exit code 127") {
		t.Errorf("pod-readiness message = %q, want it to mention exit code 127", got.Message)
	}
}

// TestValidateDeploymentPodReadinessFailsOnCrashLoop covers the case where
// restartPolicy: Always has already cycled the container back to Waiting by
// the time the check runs, so its *current* state isn't Terminated -- the
// crash is only visible via RestartCount + LastTerminationState.
func TestValidateDeploymentPodReadinessFailsOnCrashLoop(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{
			validatorBackend(testSystemNS, agentStatusHealthy, 8, 5),
			icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusInProgress, map[string]string{"inst-a": "starting"}),
		},
		[]runtime.Object{fakeCrashLoopingPod(testRequestsNS, "inst-a", "inference", 5, 127, "Error")},
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "v1", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	got := checkByName(t, out.Checks, "pod-readiness")
	if got.Status != CheckFailed {
		t.Errorf("pod-readiness = %s, want FAIL (%s)", got.Status, got.Message)
	}
	if !strings.Contains(got.Message, "restarted 5 times") || !strings.Contains(got.Message, "exit code 127") {
		t.Errorf("pod-readiness message = %q, want it to mention the restart count and exit code", got.Message)
	}
}

func TestValidateDeploymentPodReadinessFailsWhenPodMissing(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{
			validatorBackend(testSystemNS, agentStatusHealthy, 8, 5),
			icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusInProgress, map[string]string{"inst-a": "starting"}),
		},
		nil,
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "v1", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	if got := checkByName(t, out.Checks, "pod-readiness"); got.Status != CheckFailed {
		t.Errorf("pod-readiness = %s, want FAIL (%s)", got.Status, got.Message)
	}
}

// TestValidateDeploymentPodReadinessFallsBackForMiniServiceInstances confirms
// MiniService (Helm)-backed instances -- which aren't a single Pod -- still
// use the CR-reported status instead of attempting (and failing) a Pod fetch.
func TestValidateDeploymentPodReadinessFallsBackForMiniServiceInstances(t *testing.T) {
	req := icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusCompleted, nil)
	unstructured.SetNestedMap(req.Object, map[string]interface{}{
		"inst-a": map[string]interface{}{"id": "inst-a", "type": "MiniService", "status": "Active"},
	}, "status", "instances")

	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5), req},
		nil,
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "v1", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	if got := checkByName(t, out.Checks, "pod-readiness"); got.Status != CheckPassed {
		t.Errorf("pod-readiness = %s, want PASS (%s)", got.Status, got.Message)
	}
}

func TestValidateDeploymentQueueHealthWarnsOnDeploying(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{
			validatorBackend(testSystemNS, agentStatusHealthy, 8, 5),
			icmsWithInstances(testRequestsNS, "r1", "fn-1", "v1", statusInProgress, map[string]string{"inst-a": "Running"}),
		},
		[]runtime.Object{fakePod(testRequestsNS, "inst-a", corev1.ConditionTrue)},
	)

	out, err := v.ValidateDeployment(context.Background(), "fn-1", "", ValidateOptions{BackendNS: testBackendNS})
	if err != nil {
		t.Fatalf("ValidateDeployment returned error: %v", err)
	}
	if got := checkByName(t, out.Checks, "queue-health"); got.Status != CheckWarning {
		t.Errorf("queue-health = %s, want WARN (%s)", got.Status, got.Message)
	}
	if got := checkByName(t, out.Checks, "pod-readiness"); got.Status != CheckPassed {
		t.Errorf("pod-readiness = %s, want PASS (%s)", got.Status, got.Message)
	}
	if got := checkByName(t, out.Checks, "gpu-utilization"); got.Status != CheckPassed {
		t.Errorf("gpu-utilization = %s, want PASS (%s)", got.Status, got.Message)
	}
}

func TestValidateDeploymentNotFound(t *testing.T) {
	v := newFakeValidator(nil,
		[]runtime.Object{validatorBackend(testSystemNS, agentStatusHealthy, 8, 5)},
		nil,
	)
	if _, err := v.ValidateDeployment(context.Background(), "missing", "", ValidateOptions{BackendNS: testBackendNS}); err == nil {
		t.Fatal("expected error for a function with no scheduled request")
	}
}
