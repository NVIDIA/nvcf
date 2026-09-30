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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// leftovers lists what one run left behind, by kind, other than its Job and
// pod (which the Job's deadline and TTL remove).
func leftovers(t *testing.T, client *fake.Clientset, runID string) map[string][]string {
	t.Helper()
	ctx := context.Background()
	opts := metav1.ListOptions{LabelSelector: clusterValidatorRunLabel + "=" + runID}
	out := map[string][]string{}
	sas, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).List(ctx, opts)
	require.NoError(t, err)
	for _, o := range sas.Items {
		out["ServiceAccount"] = append(out["ServiceAccount"], o.Name)
	}
	crs, err := client.RbacV1().ClusterRoles().List(ctx, opts)
	require.NoError(t, err)
	for _, o := range crs.Items {
		out["ClusterRole"] = append(out["ClusterRole"], o.Name)
	}
	crbs, err := client.RbacV1().ClusterRoleBindings().List(ctx, opts)
	require.NoError(t, err)
	for _, o := range crbs.Items {
		out["ClusterRoleBinding"] = append(out["ClusterRoleBinding"], o.Name)
	}
	secrets, err := client.CoreV1().Secrets(clusterValidatorNamespace).List(ctx, opts)
	require.NoError(t, err)
	for _, o := range secrets.Items {
		out["Secret"] = append(out["Secret"], o.Name)
	}
	cms, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).List(ctx, opts)
	require.NoError(t, err)
	for _, o := range cms.Items {
		out["ConfigMap"] = append(out["ConfigMap"], o.Name)
	}
	return out
}

// lifecycleClient drives a Job through the given terminal behaviour. The Job
// gets a UID, as a real apiserver assigns one, so ownerReferences can be set.
func lifecycleClient(jobStatus func(name string) *batchv1.Job, waiting string) *fake.Clientset {
	client := fake.NewSimpleClientset()
	var jobName atomic.Value
	jobName.Store("")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		job.UID = types.UID("uid-" + job.Name)
		jobName.Store(job.Name)
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		name := jobName.Load().(string)
		if name == "" || action.(ktesting.GetAction).GetName() != name || jobStatus == nil {
			return false, nil, nil
		}
		job := jobStatus(name)
		job.Labels = clusterValidatorLabels()
		return true, job, nil
	})
	client.PrependReactor("list", "pods", podListReactor(waiting))
	// Deleting the Job takes its pod with it, as the garbage collector would;
	// the fake clientset has none.
	var jobDeleted atomic.Bool
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		jobDeleted.Store(true)
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if jobDeleted.Load() {
			return true, &corev1.PodList{}, nil
		}
		return false, nil, nil
	})
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "" {
			return false, nil, nil
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: action.(ktesting.GetAction).GetName(), Namespace: clusterValidatorNamespace,
		}}
		if waiting != "" {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  clusterValidatorContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}},
			}}
		} else {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
				Name:  clusterValidatorContainer,
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}},
			}}
		}
		return true, pod, nil
	})
	return client
}

func succeeded(name string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace},
		Status:     batchv1.JobStatus{Succeeded: 1},
	}
}

func running(name string) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: clusterValidatorNamespace}}
}

// A clean run removes everything it created apart from the Job, which its TTL
// removes: the RBAC, the minted NGC pull secret and the network-check
// ConfigMap.
func TestRunClusterValidator_HappyPathLeavesNothingBehind(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.NoError(t, res.Err)
	require.NotEmpty(t, res.RunID)
	assert.Empty(t, leftovers(t, client, res.RunID))
}

// A bootstrap that fails after the ServiceAccount exists must not abandon it.
func TestRunClusterValidator_FailedBootstrapLeavesNothingBehind(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "clusterroles", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}, "", nil)
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	sas, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, sas.Items, "the ServiceAccount created before the failure must be removed")
}

// Under --no-cleanup a failed bootstrap keeps what it created, so the result
// still carries the run ID the removal command selects on.
func TestRunClusterValidator_NoCleanupFailedBootstrapReportsTheRun(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "clusterroles", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}, "", nil)
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	require.NotEmpty(t, res.RunID)
	assert.Len(t, leftovers(t, client, res.RunID)["ServiceAccount"], 1)
}

// A pull failure means the container never started, so nothing uses the RBAC,
// the NGC pull secret or the ConfigMap: the Job is deleted and all of them go
// now, instead of waiting 30 minutes for another run's orphan sweep.
func TestRunClusterValidator_PullFailureLeavesNothingBehind(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "ImagePullBackOff")

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Empty(t, leftovers(t, client, res.RunID))
	created, deleted := "", ""
	for _, a := range client.Actions() {
		switch {
		case a.GetVerb() == "create" && a.GetResource().Resource == "jobs":
			created = a.(ktesting.CreateAction).GetObject().(*batchv1.Job).Name
		case a.GetVerb() == "delete" && a.GetResource().Resource == "jobs":
			deleted = a.(ktesting.DeleteAction).GetName()
		}
	}
	require.NotEmpty(t, created)
	assert.Equal(t, created, deleted, "the Job must be deleted so its pod stops retrying the pull")
	assert.Empty(t, res.JobName, "no hint may point at the Job that was just deleted")
}

// While the pod may still be running its RBAC must stay, but the Job owns the
// pull secret and ConfigMap so its deadline and TTL take them too.
func TestRunClusterValidator_RunningPodKeepsRBACAndJobOwnsArtifacts(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.True(t, res.LeftBehind, "the caller prints the command that removes what was kept")
	left := leftovers(t, client, res.RunID)
	assert.Len(t, left["ServiceAccount"], 1, "a running pod must keep its RBAC")
	require.Len(t, left["Secret"], 1)
	require.Len(t, left["ConfigMap"], 1)

	secret, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(context.Background(), left["Secret"][0], metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, secret.OwnerReferences, 1)
	assert.Equal(t, "Job", secret.OwnerReferences[0].Kind)
	assert.Equal(t, res.JobName, secret.OwnerReferences[0].Name)
	cm, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(context.Background(), left["ConfigMap"][0], metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, cm.OwnerReferences, 1)
	assert.Equal(t, res.JobName, cm.OwnerReferences[0].Name)
}

// The deferred sweep removes only this run's Secret. A concurrent same-role
// run's Secret must survive it, or that run's pod fails its pull mid-flight.
func TestRunClusterValidator_SparesConcurrentSameRoleSecret(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	other := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:              validatorPullSecretRunName(clusterValidatorComputePlaneRole, "other"),
			Namespace:         clusterValidatorNamespace,
			Labels:            clusterValidatorRunLabels(clusterValidatorComputePlaneRole, "other", false),
			CreationTimestamp: metav1.Now(),
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)},
	}
	client := lifecycleClient(succeeded, "")
	_, err := client.CoreV1().Secrets(clusterValidatorNamespace).Create(context.Background(), other, metav1.CreateOptions{})
	require.NoError(t, err)

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	_, err = client.CoreV1().Secrets(clusterValidatorNamespace).Get(context.Background(), other.Name, metav1.GetOptions{})
	assert.NoError(t, err, "another run's Secret must survive this run's sweep")
}

// --no-cleanup keeps everything, labelled with the run so the printed command
// removes exactly that run.
func TestRunClusterValidator_NoCleanupKeepsRunAndPrintsTheCommand(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorControlPlaneRole, nil, nil)
	require.NoError(t, res.Err)
	left := leftovers(t, client, res.RunID)
	for _, kind := range []string{"ServiceAccount", "ClusterRole", "ClusterRoleBinding", "Secret", "ConfigMap"} {
		assert.Len(t, left[kind], 1, kind)
	}
	hint := validatorCleanupHint("ctx-a", res.RunID)
	assert.Contains(t, hint, clusterValidatorRunLabel+"="+res.RunID)
	assert.Contains(t, hint, "clusterrole,clusterrolebinding")
	assert.Contains(t, hint, "--context ctx-a")
}

func stampAge(o metav1.Object, age time.Duration) {
	o.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-age)))
}

// Preserved objects are kept for a day, not forever, and the orphan sweep
// reclaims a preserved Job too. Objects labelled by a released CLI, including
// its fixed legacy name, are never touched.
func TestSweepOrphanClusterValidatorRBAC_PreservedExpireAndLegacyIsSpared(t *testing.T) {
	name := func(run string) string { return clusterValidatorRBACName(clusterValidatorControlPlaneRole, run) }
	sa := func(n string, lbls map[string]string, age time.Duration) *corev1.ServiceAccount {
		o := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: clusterValidatorNamespace, Labels: lbls}}
		stampAge(o, age)
		return o
	}
	job := func(n string, lbls map[string]string, age time.Duration) *batchv1.Job {
		o := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: clusterValidatorNamespace, Labels: lbls}}
		stampAge(o, age)
		return o
	}
	legacy := map[string]string{
		"app.kubernetes.io/name": clusterValidatorAppLabel, "app.kubernetes.io/managed-by": "nvcf-cli",
		"app.kubernetes.io/component": "preflight",
	}
	client := fake.NewSimpleClientset(
		sa(name("old-kept"), clusterValidatorRunLabels(clusterValidatorControlPlaneRole, "old-kept", true), 25*time.Hour),
		sa(name("new-kept"), clusterValidatorRunLabels(clusterValidatorControlPlaneRole, "new-kept", true), time.Hour),
		sa(clusterValidatorName, legacy, 1000*time.Hour),
		job(clusterValidatorName+"-1", clusterValidatorRunLabels(clusterValidatorControlPlaneRole, "old-kept", true), 25*time.Hour),
		job(clusterValidatorName+"-2", clusterValidatorRunLabels(clusterValidatorControlPlaneRole, "new-kept", true), time.Hour),
	)

	sweepOrphanClusterValidatorRBAC(context.Background(), client, orphanValidatorRBACTTL)

	ctx := context.Background()
	_, err := client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Get(ctx, name("old-kept"), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a preserved object older than a day is reclaimed")
	_, err = client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Get(ctx, name("new-kept"), metav1.GetOptions{})
	assert.NoError(t, err, "a preserved object younger than a day is kept")
	_, err = client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Get(ctx, clusterValidatorName, metav1.GetOptions{})
	assert.NoError(t, err, "a released CLI's fixed-name object is never touched")
	_, err = client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, clusterValidatorName+"-1", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "a preserved Job older than a day is reclaimed")
	_, err = client.BatchV1().Jobs(clusterValidatorNamespace).Get(ctx, clusterValidatorName+"-2", metav1.GetOptions{})
	assert.NoError(t, err)
}

// Forwarded settings reach the container in a stable order, empty values are
// dropped, and nothing forwarded can override what the Job itself sets.
func TestBuildClusterValidatorJob_ForwardsEnvWithoutOverridingCore(t *testing.T) {
	job := buildClusterValidatorJob("j", "img:1", "", clusterValidatorControlPlaneRole, "runid", false, map[string]string{
		"NVCF_OPENBAO_NAMESPACE": "openbao",
		"VALIDATOR_POST_INSTALL": "true",
		"VALIDATOR_ROLE":         "compute-plane",
		"NVCF_GATEWAY_NAMES":     "",
	})
	env := map[string]string{}
	var order []string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
		order = append(order, e.Name)
	}
	assert.Equal(t, "openbao", env["NVCF_OPENBAO_NAMESPACE"])
	assert.Equal(t, "true", env["VALIDATOR_POST_INSTALL"])
	assert.Equal(t, clusterValidatorControlPlaneRole, env["VALIDATOR_ROLE"], "a forwarded value must not override the role")
	_, hasEmpty := env["NVCF_GATEWAY_NAMES"]
	assert.False(t, hasEmpty, "empty values are not forwarded")
	assert.Equal(t, []string{"VALIDATOR_CONFIG_NAMESPACE", "VALIDATOR_CONFIG_NAME", "VALIDATOR_PREFLIGHT",
		"VALIDATOR_ROLE", "NVCF_OPENBAO_NAMESPACE", "VALIDATOR_POST_INSTALL"}, order)
}

// The validator discovers NVCF Gateways from every route kind and reads the
// Gateways' classes, so the CLI's ClusterRole must allow all of them.
func TestEnsureClusterValidatorRBAC_GrantsGatewayAPIReads(t *testing.T) {
	client := fake.NewSimpleClientset()
	require.NoError(t, ensureClusterValidatorRBAC(context.Background(), client, clusterValidatorControlPlaneRole, "runid", false))
	cr, err := client.RbacV1().ClusterRoles().Get(context.Background(),
		clusterValidatorRBACName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
	require.NoError(t, err)
	granted := map[string]bool{}
	for _, r := range cr.Rules {
		for _, g := range r.APIGroups {
			if g != "gateway.networking.k8s.io" {
				continue
			}
			for _, res := range r.Resources {
				granted[res] = true
			}
		}
	}
	for _, res := range []string{"gateways", "httproutes", "grpcroutes", "tcproutes", "udproutes"} {
		assert.True(t, granted[res], res)
	}
}

// The control-plane validator receives the enumerated registries and the
// forwarded env; the compute-plane one gets the env but no registries.
func TestClusterValidatorCheck_PassesRegistriesAndEnv(t *testing.T) {
	var got ClusterValidatorParams
	cv := func(_ context.Context, p ClusterValidatorParams) ClusterValidatorResult {
		got = p
		return ClusterValidatorResult{Passed: true}
	}
	regs := []RegistryEntry{{Registry: "harbor.corp.example"}}
	env := map[string]string{"VALIDATOR_POST_INSTALL": "true"}
	clusterValidatorCheck(cv, "", "img:1", "", false, validatorRoleControlPlane, regs, env).Run(context.Background())
	assert.Equal(t, regs, got.Registries)
	assert.Equal(t, env, got.Env)
}

// An image that predates validator roles runs the GPU checks on the control
// plane and fails. Without the role line in its transcript that is the image
// being too old, not the cluster failing, so it is a warning.
func TestClusterValidatorCheck_PreRoleImageIsAWarning(t *testing.T) {
	run := func(logs string, passed bool) CheckResult {
		cv := func(context.Context, ClusterValidatorParams) ClusterValidatorResult {
			return ClusterValidatorResult{Passed: passed, ExitCode: 1, Logs: logs}
		}
		return clusterValidatorCheck(cv, "", "img:1", "", false, validatorRoleControlPlane, nil, nil).Run(context.Background())
	}

	legacy := func(summaryRows ...string) string {
		return "Starting NVCF cluster validation\n=== GPU Resources ===\nno GPUs found\n" +
			"=== Validation Summary ===\nCheck Results:\n" + strings.Join(summaryRows, "\n") +
			"\nCluster is NVCF-Not-Ready\n"
	}
	old := run(legacy("\u2713    Control Plane: Healthy", "\u2717    GPU Resources: Not Available"), false)
	assert.Equal(t, SeverityWarning, old.Severity)
	assert.Contains(t, old.Message, "does not support the control-plane checks")

	// The old image's checks common to both roles still see real failures,
	// and those must not be downgraded with the GPU row.
	readyz := run(legacy("\u2717    Control Plane: Unhealthy", "\u2717    GPU Resources: Not Available"), false)
	assert.Equal(t, SeverityError, readyz.Severity)
	assert.Contains(t, readyz.Message, "Control Plane: Unhealthy")
	assert.NotContains(t, readyz.Message, "GPU Resources")

	noSummary := run("Starting NVCF cluster validation\n=== GPU Resources ===\nno GPUs found\n", false)
	assert.Equal(t, SeverityError, noSummary.Severity, "without its summary nothing shows the rest passed")

	// A role-aware image that fails before printing its role line prints no
	// GPU section either, so its failure must stand.
	early := run("level=fatal msg=\"Failed to create Kubernetes client\"\n", false)
	assert.Equal(t, SeverityError, early.Severity, "a missing role line alone is not proof of an old image")

	current := run("Starting NVCF cluster validation\nValidator role: control-plane\nTier-1: failed\n", false)
	assert.Equal(t, SeverityError, current.Severity, "a role-aware image's failure is a real failure")

	unread := run("", false)
	assert.Equal(t, SeverityError, unread.Severity, "an empty transcript proves nothing about the image")
}

// An interrupt cancels the caller's context. The operator abandoned the run,
// so the Job is stopped and everything reclaimed now, rather than leaving a
// cluster-wide ClusterRole bound in default until a later orphan sweep.
func TestRunClusterValidator_InterruptLeavesNothingBehind(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Empty(t, leftovers(t, client, res.RunID))
	deleted := false
	for _, a := range client.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "jobs" {
			deleted = true
		}
	}
	assert.True(t, deleted, "the Job must be stopped so its pod stops using the RBAC")
}

// The run's ClusterRole grants only what the validator calls. It never creates
// Services, reads pod logs or watches anything.
func TestEnsureClusterValidatorRBAC_LeastPrivilege(t *testing.T) {
	client := fake.NewSimpleClientset()
	require.NoError(t, ensureClusterValidatorRBAC(context.Background(), client, clusterValidatorControlPlaneRole, "runid", false))
	cr, err := client.RbacV1().ClusterRoles().Get(context.Background(),
		clusterValidatorRBACName(clusterValidatorControlPlaneRole, "runid"), metav1.GetOptions{})
	require.NoError(t, err)
	sawEvents := false
	for _, r := range cr.Rules {
		for _, v := range r.Verbs {
			assert.NotEqual(t, "watch", v, "no rule needs watch")
		}
		for _, res := range r.Resources {
			assert.NotEqual(t, "pods/log", res)
			if res == "services" {
				assert.ElementsMatch(t, []string{"get", "list"}, r.Verbs, "services are only read")
			}
			if res == "events" {
				assert.Equal(t, []string{"list"}, r.Verbs, "the probe only lists its pods' events")
				sawEvents = true
			}
		}
	}
	assert.True(t, sawEvents, "the overlay probe needs to list events")
}

// Only the control-plane Job reads a network-check ConfigMap. The compute-plane
// Job gets a non-empty name that resolves to nothing: an empty value would let
// the validator fall back to its default name and pick up control-plane config.
func TestBuildClusterValidatorJob_ConfigNamePerRole(t *testing.T) {
	configName := func(role string) string {
		job := buildClusterValidatorJob("j", "img:1", "", role, "runid", false, nil)
		for _, e := range job.Spec.Template.Spec.Containers[0].Env {
			if e.Name == "VALIDATOR_CONFIG_NAME" {
				return e.Value
			}
		}
		return ""
	}
	assert.Equal(t, clusterValidatorConfigRunName("runid"), configName(clusterValidatorControlPlaneRole))
	cp := configName(clusterValidatorComputePlaneRole)
	assert.Equal(t, clusterValidatorNoConfigName, cp)
	assert.NotEqual(t, clusterValidatorConfigName, cp, "must not be the validator's default name")
}

// An apiserver that stops answering after the bootstrap fails must not hold
// the CLI open in the deferred sweeps: each one is bounded, and the run
// returns once they time out.
func TestRunClusterValidator_CleanupIsBoundedWhenTheAPIServerHangs(t *testing.T) {
	prev := validatorCleanupTimeout
	validatorCleanupTimeout = 500 * time.Millisecond
	t.Cleanup(func() { validatorCleanupTimeout = prev })

	var hang atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang.Load() {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/clusterroles"):
			// The bootstrap fails here, after its ServiceAccount exists, and
			// the apiserver goes silent for everything after.
			hang.Store(true)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
		case r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":500}`)
		}
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	// JSON, so the server can echo a created object back as the response.
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host:          srv.URL,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	require.NoError(t, err)

	done := make(chan ClusterValidatorResult, 1)
	go func() {
		done <- runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
			"", false, clusterValidatorComputePlaneRole, nil, nil)
	}()
	select {
	case res := <-done:
		require.Error(t, res.Err)
		assert.Contains(t, res.Err.Error(), "create cluster role", "the run must reach the hang, not fail earlier")
		assert.True(t, hang.Load())
	case <-time.After(10 * time.Second):
		t.Fatal("the deferred cleanup did not return against a hung apiserver")
	}
}

// A Job that succeeds with the validator's own warnings is not a clean pass. A
// rollout in progress is marked transient, so --wait keeps polling on it; any
// other warning is not, so --wait does not poll forever on it.
func TestClusterValidatorCheck_PassWithWarnings(t *testing.T) {
	run := func(logs string) CheckResult {
		cv := func(context.Context, ClusterValidatorParams) ClusterValidatorResult {
			return ClusterValidatorResult{Passed: true, Logs: logs}
		}
		return clusterValidatorCheck(cv, "", "img:1", "", false, validatorRoleControlPlane, nil, nil).Run(context.Background())
	}
	base := "Validator role: control-plane\n"

	rolling := run(base + "Tier-2 StatefulSets: nats-system/nats: rolling update in progress (ready: 2/3)\n" +
		"Cluster is NVCF-Ready (with warnings)\n")
	assert.False(t, rolling.Passed)
	assert.Equal(t, SeverityWarning, rolling.Severity)
	assert.True(t, rolling.Transient)

	other := run(base + "Node-to-Node: not applicable (one Ready, schedulable node)\nCluster is NVCF-Ready (with warnings)\n")
	assert.Equal(t, SeverityWarning, other.Severity)
	assert.False(t, other.Transient, "a permanent warning must not keep --wait polling")

	clean := run(base + "Cluster is NVCF-Ready\n")
	assert.True(t, clean.Passed)
	assert.Equal(t, SeverityInfo, clean.Severity)
}

// jobDeletes returns the DeleteOptions of every Job delete the client saw.
func jobDeletes(client *fake.Clientset) []metav1.DeleteOptions {
	var out []metav1.DeleteOptions
	for _, a := range client.Actions() {
		if d, ok := a.(ktesting.DeleteActionImpl); ok && d.GetResource().Resource == "jobs" {
			out = append(out, d.DeleteOptions)
		}
	}
	return out
}

// An interrupt stops the Job in the foreground, so its pod gets SIGTERM and
// its grace period while it still has the RBAC it needs to remove its own
// probe resources. Background propagation revoked that RBAC first.
func TestRunClusterValidator_InterruptStopsTheJobInTheForeground(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	ctx, cancel := context.WithCancel(context.Background())
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})
	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	deletes := jobDeletes(client)
	require.NotEmpty(t, deletes)
	require.NotNil(t, deletes[0].PropagationPolicy)
	assert.Equal(t, metav1.DeletePropagationForeground, *deletes[0].PropagationPolicy)
	assert.Empty(t, leftovers(t, client, res.RunID), "the pod ended, so everything else goes")
}

// If the stopped pod does not end within the bound, its RBAC stays rather than
// being pulled from under it, and the caller is told what was kept.
func TestRunClusterValidator_PodThatWillNotEndKeepsItsRBAC(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		sel := action.(ktesting.ListAction).GetListRestrictions().Labels.String()
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: clusterValidatorNamespace,
				Labels: map[string]string{"job-name": strings.TrimPrefix(sel, "job-name=")}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	client.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})
	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.True(t, res.LeftBehind)
	assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1)
}

// On a plain timeout the Job's own deadline ends the pod shortly after; once it
// has, the RBAC is swept as on success instead of waiting for a later sweep.
func TestRunClusterValidator_TimeoutSweepsOnceThePodEnds(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	var ended atomic.Bool
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if !ended.Load() {
			return false, nil, nil
		}
		return true, &corev1.PodList{Items: []corev1.Pod{{Status: corev1.PodStatus{Phase: corev1.PodFailed}}}}, nil
	})
	prev := clusterValidatorTimeout
	clusterValidatorTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clusterValidatorTimeout = prev })
	go func() {
		time.Sleep(time.Second)
		ended.Store(true)
	}()

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.False(t, res.LeftBehind)
	assert.Empty(t, leftovers(t, client, res.RunID)["ClusterRole"], "the deadline ended the pod, so its RBAC goes")
}

// Under --no-cleanup the kept Secret and ConfigMap are not owned by the kept
// Job: re-running that Job by replacing it would garbage-collect them. A pull
// failure suspends the kept Job, so its pod stops retrying forever, and an
// interrupt keeps everything.
func TestRunClusterValidator_NoCleanupKeepsArtifactsUnowned(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorControlPlaneRole, nil, nil)
	require.NoError(t, res.Err)
	left := leftovers(t, client, res.RunID)
	require.Len(t, left["Secret"], 1)
	require.Len(t, left["ConfigMap"], 1)
	sec, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(context.Background(), left["Secret"][0], metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, sec.OwnerReferences)
	cm, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(context.Background(), left["ConfigMap"][0], metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, cm.OwnerReferences)

	pulling := lifecycleClient(running, "ImagePullBackOff")
	res = runClusterValidator(context.Background(), pulling, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Empty(t, jobDeletes(pulling), "a kept Job is not deleted")
	suspended := false
	for _, a := range pulling.Actions() {
		if p, ok := a.(ktesting.PatchAction); ok && p.GetResource().Resource == "jobs" &&
			strings.Contains(string(p.GetPatch()), `"suspend":true`) {
			suspended = true
		}
	}
	assert.True(t, suspended, "the kept Job must stop retrying the pull")

	interrupted := lifecycleClient(running, "")
	ctx, cancel := context.WithCancel(context.Background())
	interrupted.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})
	res = runClusterValidator(ctx, interrupted, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Empty(t, jobDeletes(interrupted), "--no-cleanup keeps the Job even on an interrupt")
}

// The operator's own pull Secret is never made a dependent of the Job: only
// the Secret this run minted is, so a Job TTL cannot garbage-collect theirs.
func TestRunClusterValidator_OperatorPullSecretIsNeverOwned(t *testing.T) {
	client := lifecycleClient(succeeded, "")
	_, err := client.CoreV1().Secrets(clusterValidatorNamespace).Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pull", Namespace: clusterValidatorNamespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"my-pull", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	sec, err := client.CoreV1().Secrets(clusterValidatorNamespace).Get(context.Background(), "my-pull", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, sec.OwnerReferences)
}

// One failed Job read is not the Job's result, and a first pull error is what
// a transient registry blip looks like: both are retried. A malformed image
// reference never pulls, so it ends the wait at once.
func TestWaitForClusterValidatorJob_TransientErrorsAreRetried(t *testing.T) {
	client := lifecycleClient(succeeded, "")
	calls := 0
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls <= 2 {
			return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
		}
		return false, nil, nil
	})
	_, err := client.BatchV1().Jobs(clusterValidatorNamespace).Create(context.Background(),
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: clusterValidatorNamespace}}, metav1.CreateOptions{})
	require.NoError(t, err)
	job, err := waitForClusterValidatorJob(context.Background(), client, "j")
	require.NoError(t, err)
	assert.Equal(t, int32(1), job.Status.Succeeded)

	prev := validatorPullFailureGrace
	validatorPullFailureGrace = time.Hour
	t.Cleanup(func() { validatorPullFailureGrace = prev })
	pulling := lifecycleClient(running, "ErrImagePull")
	_, err = pulling.BatchV1().Jobs(clusterValidatorNamespace).Create(context.Background(),
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: clusterValidatorNamespace}}, metav1.CreateOptions{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = waitForClusterValidatorJob(ctx, pulling, "j")
	var pullErr *validatorImagePullError
	assert.False(t, errors.As(err, &pullErr), "a pull error inside the grace period is retried")

	invalid := lifecycleClient(running, "InvalidImageName")
	_, err = invalid.BatchV1().Jobs(clusterValidatorNamespace).Create(context.Background(),
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: clusterValidatorNamespace}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = waitForClusterValidatorJob(context.Background(), invalid, "j")
	assert.True(t, errors.As(err, &pullErr), "a malformed reference never pulls")
}

// A run that times out with a pod that will not end returns within
// ClusterValidatorRunCeiling, which the check's budget is sized from. The
// ceiling counts every wait the timeout path makes, so shortening one of them
// here cannot leave the budget short in production.
func TestClusterValidatorRunCeiling_CoversTheTimeoutPath(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		sel := action.(ktesting.ListAction).GetListRestrictions().Labels.String()
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "stuck", Namespace: clusterValidatorNamespace,
				Labels: map[string]string{"job-name": strings.TrimPrefix(sel, "job-name=")}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}}}, nil
	})
	// The deadline grace dominates the margin, so a ceiling that leaves it
	// out is short of the run.
	prevTimeout, prevGrace, prevLogs, prevMargin :=
		clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorRunMargin
	clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorRunMargin =
		300*time.Millisecond, 3*time.Second, 500*time.Millisecond, time.Second
	t.Cleanup(func() {
		clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorRunMargin =
			prevTimeout, prevGrace, prevLogs, prevMargin
	})

	start := time.Now()
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.True(t, res.LeftBehind, "the pod never ended, so the whole grace was spent")
	assert.LessOrEqual(t, time.Since(start), ClusterValidatorRunCeiling())
}
