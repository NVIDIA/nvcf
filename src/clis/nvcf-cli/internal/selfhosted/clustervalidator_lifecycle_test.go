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
	"k8s.io/client-go/kubernetes/fake"
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
	deleted := false
	for _, a := range client.Actions() {
		if a.GetVerb() == "delete" && a.GetResource().Resource == "jobs" &&
			a.(ktesting.DeleteAction).GetName() == res.JobName {
			deleted = true
		}
	}
	assert.True(t, deleted, "the Job must be deleted so its pod stops retrying the pull")
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

	old := run("Starting NVCF cluster validation\n=== GPU Resources ===\nno GPUs found\n", false)
	assert.Equal(t, SeverityWarning, old.Severity)
	assert.Contains(t, old.Message, "does not support the control-plane checks")

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
