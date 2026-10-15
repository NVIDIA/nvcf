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
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Validator constants.
const (
	// gpuResourceName is the extended resource the NVIDIA device plugin advertises
	// on each GPU node. We sum its allocatable value across nodes for gpu-capacity.
	gpuResourceName = corev1.ResourceName("nvidia.com/gpu")

	// agentStatusHealthy is the NVCFBackend status.agentStatus value that means
	// the NVCA is connected and healthy.
	agentStatusHealthy = "Healthy"

	// tlsExpiryWarnWindow is how close to expiry a TLS certificate may be before
	// tls-cert downgrades from PASS to WARN.
	tlsExpiryWarnWindow = 30 * 24 * time.Hour

	// httpProbeTimeout bounds each NVCA HTTP probe.
	httpProbeTimeout = 10 * time.Second
)

// nowFunc is the clock used for TLS expiry math. It is a var so tests can pin it.
var nowFunc = time.Now

// k8sValidator runs health checks against a compute-plane cluster. It uses the
// typed clientset for core/apps resources (nodes, secrets, the NVCA Deployment)
// and the dynamic client for the NVCFBackend and ICMSRequest custom resources,
// reusing the same field helpers as the inspector and maintainer.
type k8sValidator struct {
	cs      kubernetes.Interface
	dc      dynamic.Interface
	httpCli *http.Client // nil disables the NVCA HTTP probes
}

// NewK8sValidator returns an AgentValidator backed by the Kubernetes clientset
// and dynamic client. Pass a non-nil httpCli to enable the NVCA HTTP probes;
// when nil, the HTTP-backed checks fall back to CR-derived state.
func NewK8sValidator(cs kubernetes.Interface, dc dynamic.Interface, httpCli *http.Client) AgentValidator {
	return &k8sValidator{cs: cs, dc: dc, httpCli: httpCli}
}

// clusterContext is the resolved per-run state every cluster check reads from.
type clusterContext struct {
	backend     map[string]interface{}
	systemNS    string
	clusterID   string
	clusterName string
}

// Validate resolves the cluster from the NVCFBackend CR, then runs the selected
// cluster checks. A missing or unreadable NVCFBackend is a hard error: without
// it we cannot determine the system namespace or cluster identity, and the
// context is almost certainly not pointed at an NVCF compute-plane cluster.
func (v *k8sValidator) Validate(ctx context.Context, opts ValidateOptions) (*ValidationResult, error) {
	cc, err := v.resolveCluster(ctx, opts.BackendNS)
	if err != nil {
		return nil, err
	}

	result := &ValidationResult{
		ClusterID:   cc.clusterID,
		ClusterName: cc.clusterName,
	}

	for _, name := range AllClusterChecks {
		if !checkSelected(name, opts.CheckNames) {
			continue
		}
		res := v.runClusterCheck(ctx, name, cc, opts)
		result.Checks = append(result.Checks, res)
		if opts.FailFast && res.Status == CheckFailed {
			break
		}
	}
	return result, nil
}

// runClusterCheck dispatches one named cluster check.
func (v *k8sValidator) runClusterCheck(ctx context.Context, name string, cc *clusterContext, opts ValidateOptions) CheckResult {
	switch name {
	case CheckNVCAReachable:
		return v.checkNVCAReachable(ctx, cc, opts)
	case CheckGPUCapacity:
		return v.checkGPUCapacity(ctx, cc)
	case CheckNATSHealth:
		return v.checkNATSHealth(ctx, cc, opts)
	case CheckPullSecret:
		return v.checkPullSecret(ctx, cc)
	case CheckTLSCert:
		return v.checkTLSCert(ctx, cc)
	default:
		// Unreachable: the cmd layer validates --check against AllClusterChecks.
		return CheckResult{Name: name, Status: CheckSkipped, Message: "unknown check"}
	}
}

// resolveCluster reads the NVCFBackend CR and extracts the identity and system
// namespace the checks need. It mirrors the maintainer's ResolveCluster.
func (v *k8sValidator) resolveCluster(ctx context.Context, backendNS string) (*clusterContext, error) {
	list, err := v.dc.Resource(nvcfBackendGVR).Namespace(backendNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, wrapCRDError(err, "NVCFBackend", backendNS)
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no NVCFBackend resource found in namespace %q; is this context pointed at an NVCF compute-plane cluster (try --backend-namespace)?", backendNS)
	}

	obj := list.Items[0].Object
	return &clusterContext{
		backend:     obj,
		systemNS:    firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "systemNamespace"), defaultSystemNamespace),
		clusterID:   firstNonEmpty(nestedString(obj, "spec", "clusterConfig", "clusterId"), nestedString(obj, "spec", "clusterConfig", "clusterID")),
		clusterName: nestedString(obj, "spec", "clusterConfig", "clusterName"),
	}, nil
}

// --- cluster checks ---

// checkNVCAReachable verifies the NVCA Deployment has at least one Ready replica
// and, when an NVCA URL is configured, that GET /version returns 2xx.
func (v *k8sValidator) checkNVCAReachable(ctx context.Context, cc *clusterContext, opts ValidateOptions) CheckResult {
	res := CheckResult{Name: CheckNVCAReachable}

	deploy, err := v.cs.AppsV1().Deployments(cc.systemNS).Get(ctx, nvcaDeploymentName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("NVCA deployment %s/%s not found; is NVCA installed?", cc.systemNS, nvcaDeploymentName)
			return res
		}
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("failed to read NVCA deployment %s/%s: %v", cc.systemNS, nvcaDeploymentName, err)
		return res
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}
	ready := deploy.Status.ReadyReplicas
	if ready < 1 {
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("%d/%d NVCA replicas Ready", ready, desired)
		return res
	}

	// Optional HTTP probe layered on top of the in-cluster readiness signal.
	if v.httpProbeEnabled(opts) {
		code, err := v.httpProbe(ctx, opts.NVCAURL, "/version")
		if err != nil {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("%d/%d NVCA replicas Ready, but GET /version failed: %v", ready, desired, err)
			return res
		}
		if !is2xx(code) {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("%d/%d NVCA replicas Ready, but GET /version returned %d", ready, desired, code)
			return res
		}
		res.Status = CheckPassed
		res.Message = fmt.Sprintf("%d/%d NVCA replicas Ready; /version OK", ready, desired)
		return res
	}

	res.Status = CheckPassed
	res.Message = fmt.Sprintf("%d/%d NVCA replicas Ready", ready, desired)
	return res
}

// checkGPUCapacity compares the GPU count Kubernetes advertises as allocatable
// across nodes with the capacity NVCA registered in the NVCFBackend CR.
func (v *k8sValidator) checkGPUCapacity(ctx context.Context, cc *clusterContext) CheckResult {
	res := CheckResult{Name: CheckGPUCapacity}

	nodes, err := v.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("failed to list nodes: %v", err)
		return res
	}

	var k8sGPU int64
	for i := range nodes.Items {
		if q, ok := nodes.Items[i].Status.Allocatable[gpuResourceName]; ok {
			k8sGPU += q.Value()
		}
	}

	var registered int64
	for _, g := range extractGPUUsage(cc.backend) {
		registered += g.Capacity
	}

	if k8sGPU == 0 && registered == 0 {
		res.Status = CheckSkipped
		res.Message = "no GPU resources reported by Kubernetes or NVCA"
		return res
	}
	if k8sGPU == registered {
		res.Status = CheckPassed
		res.Message = fmt.Sprintf("K8s allocatable: %d | NVCA registered: %d", k8sGPU, registered)
		return res
	}
	res.Status = CheckWarning
	res.Message = fmt.Sprintf("GPU count mismatch: K8s allocatable %d != NVCA registered %d", k8sGPU, registered)
	return res
}

// checkNATSHealth probes NVCA queue/NATS health via GET /livez when an NVCA URL
// is configured, otherwise falls back to the NVCFBackend agentStatus field.
func (v *k8sValidator) checkNATSHealth(ctx context.Context, cc *clusterContext, opts ValidateOptions) CheckResult {
	res := CheckResult{Name: CheckNATSHealth}

	if v.httpProbeEnabled(opts) {
		code, err := v.httpProbe(ctx, opts.NVCAURL, "/livez")
		if err != nil {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("GET /livez failed: %v", err)
			return res
		}
		if !is2xx(code) {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("GET /livez returned %d", code)
			return res
		}
		res.Status = CheckPassed
		res.Message = "/livez OK"
		return res
	}

	agentStatus := nestedString(cc.backend, "status", "agentStatus")
	if agentStatus == agentStatusHealthy {
		res.Status = CheckPassed
		res.Message = "agentStatus: Healthy (pass --nvca-url for a live /livez probe)"
		return res
	}
	res.Status = CheckWarning
	res.Message = fmt.Sprintf("agentStatus: %s (pass --nvca-url for a live /livez probe)", orUnknown(agentStatus))
	return res
}

// checkPullSecret verifies every dockerconfigjson pull secret in the system
// namespace parses and carries at least one registry credential.
func (v *k8sValidator) checkPullSecret(ctx context.Context, cc *clusterContext) CheckResult {
	res := CheckResult{Name: CheckPullSecret}

	secrets, err := v.cs.CoreV1().Secrets(cc.systemNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("failed to list secrets in %s: %v", cc.systemNS, err)
		return res
	}

	checked := 0
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if s.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		checked++
		if err := validateDockerConfigJSON(s.Data[corev1.DockerConfigJsonKey]); err != nil {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("pull secret %s/%s is invalid: %v", cc.systemNS, s.Name, err)
			return res
		}
	}

	if checked == 0 {
		res.Status = CheckWarning
		res.Message = fmt.Sprintf("no image pull secrets found in %s", cc.systemNS)
		return res
	}
	res.Status = CheckPassed
	res.Message = fmt.Sprintf("%s valid", pluralize(checked, "pull secret"))
	return res
}

// checkTLSCert inspects every TLS secret in the system namespace and reports the
// soonest expiry: FAIL if any certificate is already expired, WARN if any
// expires within tlsExpiryWarnWindow, otherwise PASS.
func (v *k8sValidator) checkTLSCert(ctx context.Context, cc *clusterContext) CheckResult {
	res := CheckResult{Name: CheckTLSCert}

	secrets, err := v.cs.CoreV1().Secrets(cc.systemNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("failed to list secrets in %s: %v", cc.systemNS, err)
		return res
	}

	now := nowFunc()
	checked := 0
	var soonestName string
	var soonestExpiry time.Time
	for i := range secrets.Items {
		s := &secrets.Items[i]
		if s.Type != corev1.SecretTypeTLS {
			continue
		}
		cert, err := parseLeafCertificate(s.Data[corev1.TLSCertKey])
		if err != nil {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("TLS secret %s/%s is invalid: %v", cc.systemNS, s.Name, err)
			return res
		}
		checked++
		if soonestName == "" || cert.NotAfter.Before(soonestExpiry) {
			soonestName = s.Name
			soonestExpiry = cert.NotAfter
		}
	}

	if checked == 0 {
		res.Status = CheckSkipped
		res.Message = fmt.Sprintf("no TLS secrets found in %s", cc.systemNS)
		return res
	}

	switch {
	case !soonestExpiry.After(now):
		res.Status = CheckFailed
		res.Message = fmt.Sprintf("TLS secret %s/%s is expired (expired %s)", cc.systemNS, soonestName, soonestExpiry.UTC().Format(time.RFC3339))
	case soonestExpiry.Sub(now) < tlsExpiryWarnWindow:
		res.Status = CheckWarning
		res.Message = fmt.Sprintf("TLS secret %s/%s expires in %d day(s)", cc.systemNS, soonestName, daysUntil(now, soonestExpiry))
	default:
		res.Status = CheckPassed
		res.Message = fmt.Sprintf("%s valid; soonest expiry in %d day(s)", pluralize(checked, "TLS secret"), daysUntil(now, soonestExpiry))
	}
	return res
}

// --- deployment validation ---

// ValidateDeployment runs the per-deployment checks for one function version.
func (v *k8sValidator) ValidateDeployment(ctx context.Context, functionID, versionID string, opts ValidateOptions) (*DeploymentValidation, error) {
	cc, err := v.resolveCluster(ctx, opts.BackendNS)
	if err != nil {
		return nil, err
	}

	items, err := listICMSRequests(ctx, v.dc, "")
	if err != nil {
		return nil, err
	}
	sortICMSRequests(items)

	var match map[string]interface{}
	var matchVerID string
	for i := range items {
		fid, vid := functionIdentity(items[i].Object)
		if fid != functionID {
			continue
		}
		if versionID != "" && vid != versionID {
			continue
		}
		match = items[i].Object
		matchVerID = vid
		break
	}
	if match == nil {
		if versionID != "" {
			return nil, fmt.Errorf("no scheduled function found for function %s version %s", functionID, versionID)
		}
		return nil, fmt.Errorf("no scheduled function found for function %s", functionID)
	}

	out := &DeploymentValidation{
		FunctionID:        functionID,
		FunctionVersionID: matchVerID,
	}
	instances := extractInstances(match)
	out.Checks = append(out.Checks,
		checkPodReadiness(instances),
		checkQueueHealth(match, instances),
		checkGPUUtilization(cc.backend),
	)
	return out, nil
}

// checkPodReadiness fails when no instances are running or any instance reports
// an error/failed status.
func checkPodReadiness(instances []Instance) CheckResult {
	res := CheckResult{Name: "pod-readiness"}
	if len(instances) == 0 {
		res.Status = CheckFailed
		res.Message = "no instances running for this function version"
		return res
	}
	for _, in := range instances {
		if instanceUnhealthy(in) {
			res.Status = CheckFailed
			res.Message = fmt.Sprintf("instance %s is unhealthy (status=%s lastReported=%s)", in.ID, orUnknown(in.Status), orUnknown(in.LastReportedStatus))
			return res
		}
	}
	res.Status = CheckPassed
	res.Message = fmt.Sprintf("%s healthy", pluralize(len(instances), "instance"))
	return res
}

// checkQueueHealth maps the collapsed request phase to a verdict: ACTIVE passes,
// DEPLOYING/DRAINING warn (transient), FAILED fails.
func checkQueueHealth(obj map[string]interface{}, instances []Instance) CheckResult {
	res := CheckResult{Name: "queue-health"}
	requestStatus := nestedString(obj, "status", "requestStatus")
	action := nestedString(obj, "spec", "action")
	phase := DerivePhase(requestStatus, action, false, instancesTerminating(instances))
	switch phase {
	case PhaseActive:
		res.Status = CheckPassed
	case PhaseFailed:
		res.Status = CheckFailed
	default:
		res.Status = CheckWarning
	}
	res.Message = fmt.Sprintf("phase %s (requestStatus=%s)", phase, orUnknown(requestStatus))
	return res
}

// checkGPUUtilization warns when more than 90% of the cluster's registered GPU
// capacity is allocated.
func checkGPUUtilization(backend map[string]interface{}) CheckResult {
	res := CheckResult{Name: "gpu-utilization"}
	var capacity, allocated int64
	for _, g := range extractGPUUsage(backend) {
		capacity += g.Capacity
		allocated += g.Allocated
	}
	if capacity == 0 {
		res.Status = CheckSkipped
		res.Message = "no GPU capacity reported by NVCA"
		return res
	}
	pct := float64(allocated) / float64(capacity) * 100
	if pct > 90 {
		res.Status = CheckWarning
		res.Message = fmt.Sprintf("GPU utilization %.0f%% (%d/%d allocated)", pct, allocated, capacity)
		return res
	}
	res.Status = CheckPassed
	res.Message = fmt.Sprintf("GPU utilization %.0f%% (%d/%d allocated)", pct, allocated, capacity)
	return res
}

// --- HTTP probe helpers ---

func (v *k8sValidator) httpProbeEnabled(opts ValidateOptions) bool {
	return v.httpCli != nil && opts.NVCAURL != ""
}

// httpProbe issues GET baseURL+path and returns the response status code. The
// body is drained and discarded so the connection can be reused.
func (v *k8sValidator) httpProbe(ctx context.Context, baseURL, path string) (int, error) {
	url := strings.TrimRight(baseURL, "/") + path
	reqCtx, cancel := context.WithTimeout(ctx, httpProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := v.httpCli.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

func is2xx(code int) bool { return code >= 200 && code < 300 }

// --- value helpers ---

// validateDockerConfigJSON parses a .dockerconfigjson payload and ensures it
// carries at least one registry credential.
func validateDockerConfigJSON(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty .dockerconfigjson")
	}
	var doc struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("malformed .dockerconfigjson: %w", err)
	}
	if len(doc.Auths) == 0 {
		return fmt.Errorf("no registry credentials under .auths")
	}
	return nil
}

// parseLeafCertificate decodes the first CERTIFICATE PEM block in data and
// parses it as an x509 certificate.
func parseLeafCertificate(data []byte) (*x509.Certificate, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty tls.crt")
	}
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, fmt.Errorf("no CERTIFICATE block in tls.crt")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

func instanceUnhealthy(in Instance) bool {
	for _, s := range []string{in.Status, in.LastReportedStatus} {
		l := strings.ToLower(s)
		for _, bad := range []string{"error", "fail", "terminating", "unknown"} {
			if strings.Contains(l, bad) {
				return true
			}
		}
	}
	return false
}

// checkSelected reports whether name should run given the --check selection.
// An empty selection runs every check.
func checkSelected(name string, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, s := range selected {
		if s == name {
			return true
		}
	}
	return false
}

func daysUntil(now, t time.Time) int {
	return int(t.Sub(now).Hours() / 24)
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
