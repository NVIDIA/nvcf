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

package selfhosted

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	clusterValidatorNamespace    = "default"
	clusterValidatorName         = "nvcf-preflight-validator"
	clusterValidatorContainer    = "validator"
	clusterValidatorAppLabel     = "nvcf-cluster-validator"
	clusterValidatorPollInterval = 2 * time.Second
	clusterValidatorTTLSeconds   = int32(600)
	clusterValidatorHintURL      = "https://docs.nvidia.com/nvcf/self-managed-clusters#cluster-validator"
)

// Vars (not consts) so tests can shorten without the full production budget.
var (
	clusterValidatorTimeout         = 5 * time.Minute
	clusterValidatorLogFetchTimeout = 10 * time.Second
)

// Kubelet Waiting.Reason values that mean the pod will never start without
// operator intervention. Detecting any short-circuits the 5-minute wait.
var pullFailureReasons = map[string]struct{}{
	"ImagePullBackOff":                {},
	"ErrImagePull":                    {},
	"FailedToRetrieveImagePullSecret": {},
	"InvalidImageName":                {},
	"ImageInspectError":               {},
	"RegistryUnavailable":             {},
}

var (
	ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	boxDrawingRE = regexp.MustCompile(`[\x{2500}-\x{257F}]+`)
	blankLineRE  = regexp.MustCompile(`\n{3,}`)
)

type ClusterValidatorParams struct {
	KubeContext string
	Image       string
	PullSecret  string
	NoCleanup   bool
	// Role selects which check set the validator runs: "control-plane" or
	// "compute-plane" (empty = compute-plane default). Passed to the Job as
	// VALIDATOR_ROLE. See clustervalidator.RoleControlPlane / RoleComputePlane.
	Role string
	// Registries is a list of additional "host:port" registry endpoints to
	// probe for reachability in the control-plane validator ConfigMap (in
	// addition to nvcr.io which is always included). Ignored for the
	// compute-plane role.
	Registries []string
}

// Err is non-nil only when the run failed to execute (RBAC bootstrap,
// image pull, context timeout). A Passed=false verdict from the validator
// itself leaves Err nil.
type ClusterValidatorResult struct {
	Passed   bool
	ExitCode int32
	Logs     string
	JobName  string
	Err      error
}

type ClusterValidator func(ctx context.Context, params ClusterValidatorParams) ClusterValidatorResult

// NewClusterValidator returns a ClusterValidator backed by client-go.
// Chart-independent so it works before any Helm release exists.
func NewClusterValidator() ClusterValidator {
	return func(ctx context.Context, p ClusterValidatorParams) ClusterValidatorResult {
		restCfg, err := loadKubeConfig(p.KubeContext)
		if err != nil {
			return ClusterValidatorResult{Err: fmt.Errorf("building kubeconfig: %w", err)}
		}
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return ClusterValidatorResult{Err: fmt.Errorf("building kubernetes client: %w", err)}
		}
		return runClusterValidator(ctx, client, p.Image, p.PullSecret, p.NoCleanup, p.Role, p.Registries)
	}
}

// Testable core. Pass a fake clientset to unit-test without a real cluster.
func runClusterValidator(ctx context.Context, client kubernetes.Interface, image, pullSecret string, noCleanup bool, role string, registries []string) ClusterValidatorResult {
	if image == "" {
		// Defensive: callers gate on configured image before invoking the
		// validator, so this branch shouldn't fire in normal use.
		return ClusterValidatorResult{Err: fmt.Errorf("cluster-validator image is empty")}
	}

	vctx, cancel := context.WithTimeout(ctx, clusterValidatorTimeout)
	defer cancel()

	// Resolver errors are non-fatal: fall through to the caller's value and
	// let waitForClusterValidatorJob surface ImagePullBackOff if needed.
	if resolved, err := resolveValidatorPullSecret(ctx, client, pullSecret, image); err == nil {
		pullSecret = resolved
	}
	// Sweep any pull secrets we created (mirror or env-mint) after the
	// Job terminates. Uses a fresh context so cleanup runs even when
	// vctx has expired. Operator-supplied secrets via the flag aren't
	// labeled by us and are skipped.
	defer sweepManagedPullSecrets(context.Background(), client)

	if err := ensureClusterValidatorRBAC(vctx, client); err != nil {
		return ClusterValidatorResult{Err: fmt.Errorf("bootstrapping validator RBAC: %w", err)}
	}

	// For the control-plane role, create a ConfigMap with reachability
	// endpoints and enforcement config so the validator runs its configurable
	// checks. Best-effort: a failure here is logged but does not abort the
	// run — the validator gracefully skips configurable checks when the
	// ConfigMap is absent.
	var configNote string
	if role == clusterValidatorControlPlaneRole {
		if err := ensureClusterValidatorConfig(vctx, client, registries); err != nil {
			// Non-fatal: continue without the ConfigMap; the validator skips
			// configurable reachability and enforcement checks silently unless
			// we surface this note in the transcript.
			configNote = fmt.Sprintf("note: validator config not applied (%v); reachability checks may be skipped", err)
		}
	}

	sweepPriorClusterValidatorJobs(vctx, client)

	jobName := fmt.Sprintf("%s-%d", clusterValidatorName, time.Now().UnixNano())
	if _, err := client.BatchV1().Jobs(clusterValidatorNamespace).Create(
		vctx, buildClusterValidatorJob(jobName, image, pullSecret, role, noCleanup), metav1.CreateOptions{},
	); err != nil {
		return ClusterValidatorResult{Err: fmt.Errorf("creating validator Job: %w", err)}
	}

	final, waitErr := waitForClusterValidatorJob(vctx, client, jobName)

	// Fetch logs under a fresh ctx from the parent: vctx is expired on the
	// timeout path, and dropping the partial transcript hurts most there.
	logCtx, logCancel := context.WithTimeout(ctx, clusterValidatorLogFetchTimeout)
	defer logCancel()
	rawLogs, _ := fetchClusterValidatorLogs(logCtx, client, jobName)
	cleaned := cleanValidatorOutput(rawLogs)
	if configNote != "" {
		cleaned = configNote + "\n" + cleaned
	}

	if waitErr != nil {
		return ClusterValidatorResult{
			JobName: jobName,
			Logs:    cleaned,
			Err:     waitErr,
		}
	}

	// Separate short ctx so a slow log fetch can't burn the exit-code lookup's
	// budget and silently return -1 on clean completions.
	exitCtx, exitCancel := context.WithTimeout(ctx, 5*time.Second)
	defer exitCancel()
	return ClusterValidatorResult{
		Passed:   final.Status.Succeeded > 0,
		ExitCode: containerExitCode(exitCtx, client, jobName),
		Logs:     cleaned,
		JobName:  jobName,
	}
}

// Creates the SA/ClusterRole/ClusterRoleBinding the validator pod runs under,
// idempotent via AlreadyExists tolerance. ClusterRole uses update-or-create so
// newer CLI versions replace stale rules without the operator needing to delete.
func ensureClusterValidatorRBAC(ctx context.Context, client kubernetes.Interface) error {
	labels := clusterValidatorLabels()

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorName,
			Namespace: clusterValidatorNamespace,
			Labels:    labels,
		},
	}
	if _, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create service account: %w", err)
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: clusterValidatorName, Labels: labels},
		Rules: []rbacv1.PolicyRule{
			// Read-only: cluster inventory and configuration.
			{APIGroups: []string{""}, Resources: []string{"nodes", "configmaps"}, Verbs: []string{"get", "list", "watch"}},
			// Read + write: enforcement checks create and delete probe namespaces
			// and pods; the active-LB check creates and deletes a probe service.
			{APIGroups: []string{""}, Resources: []string{"namespaces", "pods", "services"}, Verbs: []string{"get", "list", "watch", "create", "delete"}},
			// Pod log subresource: read probe output without exec.
			{APIGroups: []string{""}, Resources: []string{"pods/log"}, Verbs: []string{"get"}},
			{APIGroups: []string{"storage.k8s.io"}, Resources: []string{"csidrivers", "storageclasses"}, Verbs: []string{"get", "list"}},
			// NetworkPolicies: read for CNI detection; write for enforcement
			// check which creates/updates/deletes policies in the temp namespace.
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"networkpolicies"}, Verbs: []string{"get", "list", "create", "update", "delete"}},
			{APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations", "validatingwebhookconfigurations"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{"apps"}, Resources: []string{"deployments", "daemonsets", "statefulsets"}, Verbs: []string{"get", "list"}},
			// Gateway API: control-plane gateway and route health checks.
			{APIGroups: []string{"gateway.networking.k8s.io"}, Resources: []string{"gatewayclasses", "gateways", "httproutes", "grpcroutes"}, Verbs: []string{"get", "list"}},
			{NonResourceURLs: []string{"/readyz", "/version", "/healthz"}, Verbs: []string{"get"}},
		},
	}
	// Update-or-Create so a future CLI version's expanded rules replace the
	// old set; AlreadyExists tolerance alone would silently keep stale rules.
	if existing, err := client.RbacV1().ClusterRoles().Get(ctx, clusterValidatorName, metav1.GetOptions{}); err == nil {
		existing.Rules = cr.Rules
		existing.Labels = cr.Labels
		if _, err := client.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update cluster role: %w", err)
		}
	} else if apierrors.IsNotFound(err) {
		if _, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create cluster role: %w", err)
		}
	} else {
		return fmt.Errorf("get cluster role: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: clusterValidatorName, Labels: labels},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      clusterValidatorName,
			Namespace: clusterValidatorNamespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     clusterValidatorName,
		},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create cluster role binding: %w", err)
	}
	return nil
}

func clusterValidatorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       clusterValidatorAppLabel,
		"app.kubernetes.io/managed-by": "nvcf-cli",
		"app.kubernetes.io/component":  "preflight",
	}
}

// Errors are swallowed: a stale Job is preferable to blocking the new run.
func sweepPriorClusterValidatorJobs(ctx context.Context, client kubernetes.Interface) {
	selector := fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/managed-by=nvcf-cli", clusterValidatorAppLabel)
	propagation := metav1.DeletePropagationBackground
	_ = client.BatchV1().Jobs(clusterValidatorNamespace).DeleteCollection(
		ctx,
		metav1.DeleteOptions{PropagationPolicy: &propagation},
		metav1.ListOptions{LabelSelector: selector},
	)
}

// sweepManagedPullSecrets removes any docker-registry secrets in
// clusterValidatorNamespace that we previously created (mirrored from
// another namespace or minted from NGC_API_KEY). Called after the Job
// terminates so NGC credentials don't persist across preflight runs.
//
// Operator-supplied secrets via --cluster-validator-pull-secret aren't
// labeled by us and are skipped by the selector. Errors are swallowed:
// failing to clean up is preferable to failing the check itself.
func sweepManagedPullSecrets(ctx context.Context, client kubernetes.Interface) {
	selector := fmt.Sprintf("app.kubernetes.io/name=%s,app.kubernetes.io/managed-by=nvcf-cli", clusterValidatorAppLabel)
	_ = client.CoreV1().Secrets(clusterValidatorNamespace).DeleteCollection(
		ctx,
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: selector},
	)
}

// clusterValidatorControlPlaneRole is the role value passed as VALIDATOR_ROLE
// when running against the control-plane cluster. Matches nvca's RoleControlPlane
// without importing that package.
const clusterValidatorControlPlaneRole = "control-plane"

// clusterValidatorConfigName is the default ConfigMap name the validator binary
// looks for when VALIDATOR_CONFIG_NAME is empty. Must stay in sync with
// defaultConfigMapName in nvca/cmd/cluster-validator/main.go.
const clusterValidatorConfigName = "cluster-validator-network-checks"

// controlPlaneValidatorConfigTemplate is the baseline network-check ConfigMap for
// control-plane preflight: nvcr.io reachability (critical) and NetworkPolicy
// enforcement (non-critical). Extra registries are appended as non-critical probes.
const controlPlaneValidatorConfigTemplate = `reachability:
  endpoints:
    - name: nvcr.io
      host: nvcr.io
      port: 443
      protocol: tcp+tls
      critical: true
enforcement:
  enabled: true
  testImage: busybox:1.36
  timeoutSeconds: 60
  critical: false
`

// ensureClusterValidatorConfig creates or updates the network-check ConfigMap.
// extraRegistries are added as non-critical tcp+tls probes. Best-effort: the
// validator skips configurable checks when the ConfigMap is absent.
func ensureClusterValidatorConfig(ctx context.Context, client kubernetes.Interface, extraRegistries []string) error {
	content := buildControlPlaneValidatorConfig(extraRegistries)
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterValidatorConfigName,
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorLabels(),
		},
		Data: map[string]string{"config.yaml": content},
	}

	existing, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(ctx, clusterValidatorConfigName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get validator config ConfigMap: %w", err)
		}
		if _, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create validator config ConfigMap: %w", err)
		}
		return nil
	}

	// Always update so a newer CLI version's config (or new registries) replaces stale content.
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	if _, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update validator config ConfigMap: %w", err)
	}
	return nil
}

// buildControlPlaneValidatorConfig assembles the network-check ConfigMap YAML
// from the baseline template plus any operator-supplied extra registries.
func buildControlPlaneValidatorConfig(extraRegistries []string) string {
	if len(extraRegistries) == 0 {
		return controlPlaneValidatorConfigTemplate
	}

	// Parse host:port entries and append as non-critical tcp+tls endpoints.
	var extra strings.Builder
	for _, reg := range extraRegistries {
		host, port := parseRegistryHostPort(reg)
		if host == "" {
			continue
		}
		// Append under the existing reachability.endpoints list.
		fmt.Fprintf(&extra, "    - name: %s\n      host: %s\n      port: %d\n      protocol: tcp+tls\n      critical: false\n", host, host, port)
	}
	if extra.Len() == 0 {
		return controlPlaneValidatorConfigTemplate
	}

	// Insert extra endpoints after the nvcr.io entry (before the enforcement block).
	return strings.Replace(controlPlaneValidatorConfigTemplate,
		"enforcement:", extra.String()+"enforcement:", 1)
}

// parseRegistryHostPort splits a "host:port" string. Returns port 443 when
// no port is specified or when the port is not a valid number.
func parseRegistryHostPort(s string) (host string, port int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	if idx := strings.LastIndex(s, ":"); idx > 0 {
		h := s[:idx]
		p := s[idx+1:]
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return s, 443
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 && n <= 65535 {
			return h, n
		}
	}
	return s, 443
}

// buildClusterValidatorJob creates the validator Job. PullIfNotPresent reuses
// locally-imported images. VALIDATOR_PREFLIGHT=true skips the summary ConfigMap
// write. VALIDATOR_ROLE selects the check set (control-plane vs compute-plane).
func buildClusterValidatorJob(name, image, pullSecret, role string, noCleanup bool) *batchv1.Job {
	backoff := int32(0)
	podSpec := corev1.PodSpec{
		ServiceAccountName: clusterValidatorName,
		RestartPolicy:      corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:            clusterValidatorContainer,
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env: []corev1.EnvVar{
				{Name: "VALIDATOR_CONFIG_NAMESPACE", Value: clusterValidatorNamespace},
				{Name: "VALIDATOR_CONFIG_NAME", Value: ""},
				{Name: "VALIDATOR_PREFLIGHT", Value: "true"},
				{Name: "VALIDATOR_ROLE", Value: role},
			},
		}},
	}
	if pullSecret != "" {
		podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: pullSecret}}
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: clusterValidatorNamespace,
			Labels:    clusterValidatorLabels(),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: clusterValidatorLabels()},
				Spec:       podSpec,
			},
		},
	}
	if !noCleanup {
		ttl := clusterValidatorTTLSeconds
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	return job
}

// Polls until Succeeded>0, Failed>0, or ctx expires. Each tick also checks
// for image-pull failure so a missing pull secret surfaces in ~30s instead
// of after the full 5m timeout.
func waitForClusterValidatorJob(ctx context.Context, client kubernetes.Interface, jobName string) (*batchv1.Job, error) {
	ticker := time.NewTicker(clusterValidatorPollInterval)
	defer ticker.Stop()
	for {
		job, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			// If the deadline fired between ticker and Get, surface the wait
			// wording the select branch would have used.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("waiting for job %s: %w", jobName, ctxErr)
			}
			return nil, fmt.Errorf("get job %s: %w", jobName, err)
		}
		if job.Status.Succeeded > 0 || job.Status.Failed > 0 {
			return job, nil
		}
		if reason, msg := podPullFailureReason(ctx, client, jobName); reason != "" {
			return job, fmt.Errorf("validator pod cannot pull image (%s): %s", reason, msg)
		}
		select {
		case <-ctx.Done():
			return job, fmt.Errorf("waiting for job %s: %w", jobName, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Returns ("", "") when no pull failure is observed; callers treat that
// as "keep waiting".
func podPullFailureReason(ctx context.Context, client kubernetes.Interface, jobName string) (reason, message string) {
	podName, _ := podNameForClusterValidatorJob(ctx, client, jobName)
	if podName == "" {
		return "", ""
	}
	pod, err := client.CoreV1().Pods(clusterValidatorNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", ""
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			if _, ok := pullFailureReasons[cs.State.Waiting.Reason]; ok {
				return cs.State.Waiting.Reason, cs.State.Waiting.Message
			}
		}
	}
	// FailedToRetrieveImagePullSecret can surface on Pod conditions before
	// any container status is reported.
	for _, cond := range pod.Status.Conditions {
		if cond.Reason == "" {
			continue
		}
		if _, ok := pullFailureReasons[cond.Reason]; ok {
			return cond.Reason, cond.Message
		}
	}
	return "", ""
}

// Returns ("", err) when the log stream cannot be opened; callers store
// whatever they got and surface the underlying waitErr.
func fetchClusterValidatorLogs(ctx context.Context, client kubernetes.Interface, jobName string) (string, error) {
	podName, err := podNameForClusterValidatorJob(ctx, client, jobName)
	if err != nil || podName == "" {
		return "", err
	}
	req := client.CoreV1().Pods(clusterValidatorNamespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: clusterValidatorContainer,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open log stream for %s: %w", podName, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read logs for %s: %w", podName, err)
	}
	return string(b), nil
}

// Filters on the job-name label rather than OwnerReferences so the helper
// works against fake clientsets that don't populate OwnerReferences.
func podNameForClusterValidatorJob(ctx context.Context, client kubernetes.Interface, jobName string) (string, error) {
	pods, err := client.CoreV1().Pods(clusterValidatorNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", fmt.Errorf("list pods for job %s: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", nil
	}
	return pods.Items[0].Name, nil
}

// Informational only; the canonical pass/fail signal is Job.Status.Succeeded.
// Returns -1 when the exit code can't be read.
func containerExitCode(ctx context.Context, client kubernetes.Interface, jobName string) int32 {
	podName, _ := podNameForClusterValidatorJob(ctx, client, jobName)
	if podName == "" {
		return -1
	}
	pod, err := client.CoreV1().Pods(clusterValidatorNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return -1
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == clusterValidatorContainer && cs.State.Terminated != nil {
			return cs.State.Terminated.ExitCode
		}
	}
	return -1
}

// Idempotent: running twice yields the same output.
func cleanValidatorOutput(raw string) string {
	if raw == "" {
		return ""
	}
	s := ansiEscapeRE.ReplaceAllString(raw, "")
	s = boxDrawingRE.ReplaceAllString(s, "")
	s = blankLineRE.ReplaceAllString(s, "\n\n")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s + "\n"
}

func kubectlLogsHint(jobName string) string {
	if jobName == "" {
		return ""
	}
	return fmt.Sprintf("kubectl logs -n %s job/%s --tail=-1", clusterValidatorNamespace, jobName)
}
