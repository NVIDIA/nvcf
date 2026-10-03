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
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
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
	roles, err := client.RbacV1().Roles(clusterValidatorNamespace).List(ctx, opts)
	require.NoError(t, err)
	for _, o := range roles.Items {
		out["Role"] = append(out["Role"], o.Name)
	}
	rbs, err := client.RbacV1().RoleBindings(clusterValidatorNamespace).List(ctx, opts)
	require.NoError(t, err)
	for _, o := range rbs.Items {
		out["RoleBinding"] = append(out["RoleBinding"], o.Name)
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
	var jobDeleted atomic.Bool
	var jobLabels atomic.Value
	jobLabels.Store(map[string]string{})
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
		if job.UID == "" {
			job.UID = types.UID("uid-" + job.Name)
		}
		jobName.Store(job.Name)
		jobLabels.Store(job.Labels)
		return false, nil, nil
	})
	client.PrependReactor("get", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		name := jobName.Load().(string)
		if name == "" || action.(ktesting.GetAction).GetName() != name || jobStatus == nil || jobDeleted.Load() {
			return false, nil, nil
		}
		job := jobStatus(name)
		job.UID = types.UID("uid-" + name)
		job.Labels = jobLabels.Load().(map[string]string)
		return true, job, nil
	})
	// The pod's phase follows the Job's: ended once the Job has finished,
	// pending while it waits on its image, running otherwise.
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		phase := corev1.PodRunning
		switch name := jobName.Load().(string); {
		case waiting != "":
			phase = corev1.PodPending
		case name != "" && jobStatus != nil && jobStatus(name).Status.Succeeded > 0:
			phase = corev1.PodSucceeded
		case name != "" && jobStatus != nil && jobStatus(name).Status.Failed > 0:
			phase = corev1.PodFailed
		}
		ret, obj, err := podListReactor(waiting)(action)
		if list, ok := obj.(*corev1.PodList); ok {
			for i := range list.Items {
				list.Items[i].Status.Phase = phase
			}
		}
		return ret, obj, err
	})
	// Deleting the Job takes its pod with it, as the garbage collector would;
	// the fake clientset has none.
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
// pull secret and ConfigMap so its deadline and TTL take them too. The
// validator's own timeout leaves it to its deadline; only the end of the run
// stops it.
func TestRunClusterValidator_RunningPodKeepsRBACAndJobOwnsArtifacts(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prev := clusterValidatorTimeout
	clusterValidatorTimeout = 200 * time.Millisecond
	t.Cleanup(func() { clusterValidatorTimeout = prev })
	client := lifecycleClient(running, "")

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "the validator did not finish within 200ms")
	assert.Empty(t, jobDeletes(client), "the Job's deadline ends the pod; nothing stops it early")
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
	for _, kind := range []string{
		"ServiceAccount", "ClusterRole", "ClusterRoleBinding", "Role", "RoleBinding", "Secret", "ConfigMap",
	} {
		assert.Len(t, left[kind], 1, kind)
	}
	assert.True(t, res.Created)
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

	for _, run := range []struct {
		id  string
		age time.Duration
	}{{"old-kept", 25 * time.Hour}, {"new-kept", time.Hour}} {
		lbls := clusterValidatorRunLabels(clusterValidatorControlPlaneRole, run.id, true)
		objs := []metav1.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: validatorPullSecretRunName(clusterValidatorControlPlaneRole,
				run.id), Namespace: clusterValidatorNamespace, Labels: lbls}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: clusterValidatorConfigRunName(run.id),
				Namespace: clusterValidatorNamespace,
				Labels:    clusterValidatorConfigLabels(clusterValidatorControlPlaneRole, run.id, true)}},
			&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name(run.id), Labels: lbls}},
			&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name(run.id), Labels: lbls}},
		}
		for _, o := range objs {
			stampAge(o, run.age)
			require.NoError(t, client.Tracker().Add(o.(runtime.Object)))
		}
	}

	sweepOrphanClusterValidatorRBAC(context.Background(), client, time.Now(), orphanValidatorRBACTTL, "", "")

	ctx := context.Background()
	for _, run := range []struct {
		id   string
		kept bool
	}{{"old-kept", false}, {"new-kept", true}} {
		_, secretErr := client.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx,
			validatorPullSecretRunName(clusterValidatorControlPlaneRole, run.id), metav1.GetOptions{})
		_, cmErr := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(ctx, clusterValidatorConfigRunName(run.id),
			metav1.GetOptions{})
		_, crErr := client.RbacV1().ClusterRoles().Get(ctx, name(run.id), metav1.GetOptions{})
		_, crbErr := client.RbacV1().ClusterRoleBindings().Get(ctx, name(run.id), metav1.GetOptions{})
		for kind, err := range map[string]error{"Secret": secretErr, "ConfigMap": cmErr, "ClusterRole": crErr,
			"ClusterRoleBinding": crbErr} {
			if run.kept {
				assert.NoError(t, err, "a preserved %s younger than a day is kept", kind)
			} else {
				assert.True(t, apierrors.IsNotFound(err), "a preserved %s older than a day is reclaimed", kind)
			}
		}
	}
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
	job := buildClusterValidatorJob("j", "img:1", "", clusterValidatorControlPlaneRole, "runid",
		clusterValidatorNoConfigName, false, map[string]string{
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

// The extra tolerations reach the Job beside the control-plane ones it always
// carries, so a cluster whose nodes use other taints can schedule it.
func TestRunClusterValidator_ExtraTolerationsReachTheJob(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")
	var job *batchv1.Job
	client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
		job = a.(ktesting.CreateAction).GetObject().(*batchv1.Job).DeepCopy()
		return false, nil, nil
	})
	extra := corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "infra",
		Effect: corev1.TaintEffectNoSchedule}
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1", "", false,
		clusterValidatorComputePlaneRole, nil, nil, extra)
	require.NoError(t, res.Err)
	require.NotNil(t, job)
	assert.Equal(t, append(clusterValidatorTolerations(), extra), job.Spec.Template.Spec.Tolerations)
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
	clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1",
		ClusterValidatorRegistries: regs, ClusterValidatorEnv: env}, validatorRoleControlPlane).Run(context.Background())
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
		return clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1"},
			validatorRoleControlPlane).Run(context.Background())
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
			// The server notices a closed connection only once the body has
			// been read, and the cleanup's deletes carry one.
			_, _ = io.Copy(io.Discard, r.Body)
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
		return clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1"},
			validatorRoleControlPlane).Run(context.Background())
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

// On a plain timeout the Job's own deadline ends the pod shortly after. The
// run reads as the validator not finishing, not as failed checks: it keeps
// the partial transcript, and once the pod is gone its RBAC is swept.
func TestRunClusterValidator_DeadlineKillIsATimeout(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	var killed atomic.Bool
	client := lifecycleClient(func(name string) *batchv1.Job {
		if !killed.Load() {
			return running(name)
		}
		job := running(name)
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded,
		}}
		return job
	}, "")
	// The deadline deletes the pod, so the re-read finds no transcript.
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if killed.Load() {
			return true, &corev1.PodList{}, nil
		}
		return false, nil, nil
	})
	prevTimeout, prevGrace := clusterValidatorTimeout, validatorDeadlineGrace
	clusterValidatorTimeout, validatorDeadlineGrace = 300*time.Millisecond, 5*time.Second
	t.Cleanup(func() { clusterValidatorTimeout, validatorDeadlineGrace = prevTimeout, prevGrace })
	timer := time.AfterFunc(time.Second, func() { killed.Store(true) })
	t.Cleanup(func() { timer.Stop() })

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.EqualError(t, res.Err, "the validator did not finish within 300ms (stopped at its active deadline)")
	assert.Equal(t, "fake logs\n", res.Logs, "the partial transcript read at the timeout is kept")
	assert.False(t, res.LeftBehind)
	assert.Empty(t, leftovers(t, client, res.RunID)["ClusterRole"], "the deadline ended the pod, so its RBAC goes")
}

// Once the Job's pod has ended, its RBAC goes even if the Job never records
// the result.
func TestRunClusterValidator_TimeoutSweepsOnceThePodEnds(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "")
	var ended atomic.Bool
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if !ended.Load() {
			return false, nil, nil
		}
		return podPhaseReactor(func() corev1.PodPhase { return corev1.PodFailed })(action)
	})
	prev := clusterValidatorTimeout
	clusterValidatorTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clusterValidatorTimeout = prev })
	timer := time.AfterFunc(time.Second, func() { ended.Store(true) })
	t.Cleanup(func() { timer.Stop() })

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.False(t, res.LeftBehind)
	assert.Empty(t, leftovers(t, client, res.RunID)["ClusterRole"], "the pod ended, so its RBAC goes")
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
	// The deadline grace dominates every other bound in the ceiling, so a
	// ceiling that leaves it out is short of the run, and a wait shorter than
	// the grace ends before it.
	prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin := clusterValidatorTimeout,
		validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout, validatorRunMargin
	clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
		validatorRunMargin = 300*time.Millisecond, 3*time.Second, 100*time.Millisecond, 100*time.Millisecond,
		100*time.Millisecond
	t.Cleanup(func() {
		clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
			validatorRunMargin = prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin
	})

	start := time.Now()
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	elapsed := time.Since(start)
	require.Error(t, res.Err)
	assert.True(t, res.LeftBehind, "the pod never ended, so the whole grace was spent")
	assert.GreaterOrEqual(t, elapsed, clusterValidatorTimeout+validatorDeadlineGrace,
		"the run waited out the deadline grace, not a shorter wait")
	assert.LessOrEqual(t, elapsed, ClusterValidatorRunCeiling())
}

// podPhaseReactor answers the Job's pod list with one pod in the phase phase()
// returns at the time of the call.
func podPhaseReactor(phase func() corev1.PodPhase) ktesting.ReactionFunc {
	return func(action ktesting.Action) (bool, runtime.Object, error) {
		jobName := strings.TrimPrefix(action.(ktesting.ListAction).GetListRestrictions().Labels.String(), "job-name=")
		return true, &corev1.PodList{Items: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: jobName + "-pod", Namespace: clusterValidatorNamespace,
				Labels: map[string]string{"job-name": jobName}},
			Status: corev1.PodStatus{Phase: phase()},
		}}}, nil
	}
}

// A validator that finishes after the CLI's own timeout, while the CLI waits
// for the Job's deadline to end its pod, is graded on what it did. That wait
// is the deadline grace, not the shorter wait for a stopped Job.
func TestRunClusterValidator_LateFinishIsGraded(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prevTimeout, prevStop, prevGrace := clusterValidatorTimeout, validatorStopTimeout, validatorDeadlineGrace
	clusterValidatorTimeout, validatorStopTimeout, validatorDeadlineGrace =
		300*time.Millisecond, 300*time.Millisecond, 5*time.Second
	t.Cleanup(func() {
		clusterValidatorTimeout, validatorStopTimeout, validatorDeadlineGrace = prevTimeout, prevStop, prevGrace
	})
	var finished atomic.Bool
	client := lifecycleClient(func(name string) *batchv1.Job {
		if finished.Load() {
			return succeeded(name)
		}
		return running(name)
	}, "")
	client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase {
		if finished.Load() {
			return corev1.PodSucceeded
		}
		return corev1.PodRunning
	}))
	timer := time.AfterFunc(1500*time.Millisecond, func() { finished.Store(true) })
	t.Cleanup(func() { timer.Stop() })

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
	assert.False(t, res.LeftBehind)
	assert.NotEmpty(t, res.JobName)
	assert.Empty(t, leftovers(t, client, res.RunID), "the pod ended, so the sweeps ran")
}

// Failed Job reads end the wait, but not the run: once the pod has ended the
// Job is read again, and a validator that passed while its Job could not be
// read passes.
func TestRunClusterValidator_JobReadOutageIsGradedOnTheJob(t *testing.T) {
	prev := clusterValidatorPollInterval
	clusterValidatorPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { clusterValidatorPollInterval = prev })
	client := lifecycleClient(succeeded, "")
	var failedReads atomic.Int32
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if failedReads.Load() < int32(validatorGetErrorLimit) {
			failedReads.Add(1)
			return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
		}
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodSucceeded }))

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
	assert.EqualValues(t, validatorGetErrorLimit, failedReads.Load(), "the outage ended the wait")
}

// Only consecutive failed reads end the wait: a good read in between starts
// the count again.
func TestWaitForClusterValidatorJob_OnlyConsecutiveReadErrorsEndTheWait(t *testing.T) {
	prev := clusterValidatorPollInterval
	clusterValidatorPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { clusterValidatorPollInterval = prev })
	client := fake.NewSimpleClientset()
	calls := 0
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		switch {
		case calls > 3*validatorGetErrorLimit:
			return true, succeeded("j"), nil
		case calls%validatorGetErrorLimit == 0:
			return true, running("j"), nil
		}
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	job, err := waitForClusterValidatorJob(ctx, client, "j")
	require.NoError(t, err)
	assert.Equal(t, int32(1), job.Status.Succeeded)
}

// A missing Job, or one this kubeconfig may not read, cannot change, so the
// first such read ends the wait.
func TestWaitForClusterValidatorJob_PermanentReadErrorsEndTheWaitAtOnce(t *testing.T) {
	prev := clusterValidatorPollInterval
	clusterValidatorPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { clusterValidatorPollInterval = prev })
	gr := schema.GroupResource{Group: batchv1.GroupName, Resource: "jobs"}
	for _, readErr := range []error{
		apierrors.NewNotFound(gr, "j"),
		apierrors.NewForbidden(gr, "j", errors.New("denied")),
		apierrors.NewUnauthorized("expired token"),
	} {
		client := fake.NewSimpleClientset()
		calls := 0
		client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			calls++
			return true, nil, readErr
		})
		_, err := waitForClusterValidatorJob(context.Background(), client, "j")
		require.Error(t, err)
		assert.Equal(t, 1, calls, "%v is not retried", readErr)
	}
}

// A pull error no retry fixes is final at once. One that can clear, a
// registry outage or a DNS failure, waits out the grace.
func TestWaitForClusterValidatorJob_PermanentPullErrorsAreFinalAtOnce(t *testing.T) {
	prevGrace, prevPoll := validatorPullFailureGrace, clusterValidatorPollInterval
	validatorPullFailureGrace, clusterValidatorPollInterval = time.Hour, 10*time.Millisecond
	t.Cleanup(func() { validatorPullFailureGrace, clusterValidatorPollInterval = prevGrace, prevPoll })
	for msg, final := range map[string]bool{
		`failed to resolve reference "nvcr.io/nvidia/validator:9": nvcr.io/nvidia/validator:9: not found`:       true,
		`failed to authorize: failed to fetch oauth token: 401 Unauthorized`:                                    true,
		`pull access denied, repository does not exist or may require authorization`:                            true,
		`failed to resolve reference "nvcr.io/nvidia/validator:9": manifest unknown`:                            true,
		`failed to fetch anonymous token: authentication required`:                                              true,
		`failed to do request: Head "https://nvcr.io/v2/nvidia/validator/manifests/9": 503 Service Unavailable`: false,
		`dial tcp: lookup nvcr.io on 10.96.0.10:53: i/o timeout`:                                                false,
	} {
		client := fake.NewSimpleClientset()
		client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, running("j"), nil
		})
		client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodPending }))
		client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
			return true, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: action.(ktesting.GetAction).GetName(), Namespace: clusterValidatorNamespace},
				Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
					Name:  clusterValidatorContainer,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: msg}},
				}}},
			}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		_, err := waitForClusterValidatorJob(ctx, client, "j")
		cancel()
		var pullErr *validatorImagePullError
		assert.Equal(t, final, errors.As(err, &pullErr), msg)
	}
}

// A pod that cannot be listed may still be running: stopping its Job does not
// revoke its RBAC until the pod is seen to end, and the result says how to
// remove what was kept.
func TestRunClusterValidator_UnlistablePodKeepsItsRBAC(t *testing.T) {
	prev := validatorStopTimeout
	validatorStopTimeout = 300 * time.Millisecond
	t.Cleanup(func() { validatorStopTimeout = prev })
	client := lifecycleClient(running, "ImagePullBackOff")
	var deleted atomic.Bool
	client.PrependReactor("delete", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		deleted.Store(true)
		return false, nil, nil
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if deleted.Load() {
			return true, nil, errors.New("connection refused")
		}
		return false, nil, nil
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	var pullErr *validatorImagePullError
	require.ErrorAs(t, res.Err, &pullErr)
	assert.True(t, res.LeftBehind)
	assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1, "the RBAC stays until the pod is seen to end")
}

// A Job create that fails may still have been applied, the response lost. Its
// Job is stopped, and when its pod does not end the objects are kept and the
// result says how to remove them. Under --no-cleanup the Job is kept, like
// everything else the run made.
func TestRunClusterValidator_FailedCreateThatWasApplied(t *testing.T) {
	prev := validatorStopTimeout
	validatorStopTimeout = 300 * time.Millisecond
	t.Cleanup(func() { validatorStopTimeout = prev })
	for _, noCleanup := range []bool{false, true} {
		client := lifecycleClient(running, "")
		client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
			job := action.(ktesting.CreateAction).GetObject().(*batchv1.Job)
			require.NoError(t, client.Tracker().Add(job))
			return true, nil, apierrors.NewInternalError(errors.New("connection reset by peer"))
		})
		client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodRunning }))

		res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
			"", noCleanup, clusterValidatorComputePlaneRole, nil, nil)
		require.ErrorContains(t, res.Err, "creating validator Job")
		require.NotEmpty(t, res.RunID, "noCleanup=%v", noCleanup)
		if noCleanup {
			assert.Empty(t, jobDeletes(client), "--no-cleanup keeps the Job")
			assert.False(t, res.LeftBehind, "the --no-cleanup command covers what was kept")
			continue
		}
		assert.NotEmpty(t, jobDeletes(client), "the applied Job is stopped")
		assert.True(t, res.LeftBehind, "its pod did not end, so its objects were kept")
		assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1)
	}
}

// A run whose budget ends while it waits out a Job read outage was cut short:
// the validator's result was still to come, so the error says the budget
// ended rather than reading as the validator's failure.
func TestRunClusterValidator_BudgetEndDuringAReadOutageIsCutShort(t *testing.T) {
	prev := clusterValidatorPollInterval
	clusterValidatorPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { clusterValidatorPollInterval = prev })
	client := lifecycleClient(running, "")
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
	})
	client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodRunning }))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.ErrorIs(t, res.Err, context.DeadlineExceeded)
	assert.ErrorContains(t, res.Err, "the apiserver is restarting", "the outage is still named")
}

// legacyValidatorTranscript is what a validator that predates roles (v3.2.26)
// prints on a cluster with no GPUs, byte for byte from its printSummary: ANSI
// colours, the box around the verdict, and the failure line after the box,
// which carries the same mark as a failed row.
func legacyValidatorTranscript(controlPlaneHealthy bool) string {
	const (
		blue, green, red, yellow, reset = "\x1b[34m", "\x1b[32m", "\x1b[31m", "\x1b[33m", "\x1b[0m"
		check, cross, warn              = "\u2713", "\u2717", "\u26a0"
	)
	sep := blue + strings.Repeat("\u2501", 60) + reset
	header := func(title string) []string { return []string{"", sep, blue + "  " + title + reset, sep} }
	edge := strings.Repeat("\u2550", 59)
	controlPlane := green + check + "    Control Plane: Healthy" + reset
	if !controlPlaneHealthy {
		controlPlane = red + cross + "    Control Plane: Unhealthy" + reset
	}
	var lines []string
	lines = append(lines, header("GPU Resources")...)
	lines = append(lines, red+cross+"  No GPU resources found in cluster"+reset)
	lines = append(lines, header("Validation Summary")...)
	lines = append(lines,
		"Check Results:",
		controlPlane,
		green+check+"    Worker Nodes: All Ready"+reset,
		green+check+"    Admission Webhooks: Mutating & Validating Supported"+reset,
		green+check+"    Network Policies: Supported"+reset,
		green+check+"    SMB CSI Driver: v1.16.0+ Installed"+reset,
		red+cross+"    GPU Resources: Not Available"+reset,
		yellow+warn+"    GPU Operator: Not Installed"+reset,
		"", sep, "",
		red+"\u2554"+edge+"\u2557"+reset,
		red+"\u2551              "+cross+"  Cluster is NVCF-Not-Ready  "+cross+"              \u2551"+reset,
		red+"\u255a"+edge+"\u255d"+reset,
		"",
		red+cross+"  Your cluster does not meet all requirements for NVCF workloads"+reset,
		"",
		"Validation completed at 2026-10-03 18:35:37 UTC",
	)
	return strings.Join(lines, "\n") + "\n"
}

// gradeWith runs the validator check for role on a stub that returns res.
func gradeWith(role string, res ClusterValidatorResult) CheckResult {
	cv := func(context.Context, ClusterValidatorParams) ClusterValidatorResult { return res }
	return clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1", KubeContext: "ctx-a"},
		role).Run(context.Background())
}

// The narrow downgrade for an image that predates roles, against what such an
// image really prints. Its GPU failure on a CPU-only control plane is the
// image's doing, and the failure line it prints after the verdict box is not
// a row; any other failed row stands. On the compute plane nothing is
// downgraded: the old image ran the right checks there.
func TestClusterValidatorCheck_LegacyTranscript(t *testing.T) {
	grade := func(role string, controlPlaneHealthy bool) CheckResult {
		return gradeWith(role, ClusterValidatorResult{ExitCode: 1, Logs: cleanValidatorOutput(
			legacyValidatorTranscript(controlPlaneHealthy))})
	}
	gpuOnly := grade(validatorRoleControlPlane, true)
	assert.Equal(t, SeverityWarning, gpuOnly.Severity, gpuOnly.Message)
	assert.Contains(t, gpuOnly.Message, "does not support the control-plane checks")

	both := grade(validatorRoleControlPlane, false)
	assert.Equal(t, SeverityError, both.Severity)
	assert.Equal(t, "cluster-validator reported failures (Control Plane: Unhealthy); the image predates "+
		"validator roles, so only its checks common to both roles ran", both.Message)

	compute := grade(validatorRoleComputePlane, true)
	assert.Equal(t, SeverityError, compute.Severity)
	assert.Equal(t, "cluster-validator reported failures: GPU Resources: Not Available", compute.Message)
}

// The legacy test needs both halves of its evidence: no role line, and the
// compute-plane check set's output.
func TestClusterValidatorCheck_LegacyNeedsNoRoleLineAndTheGPUSet(t *testing.T) {
	roleAware := gradeWith(validatorRoleControlPlane, ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: " +
		"control-plane\n=== GPU Resources ===\nCheck Results:\n\u2717    GPU Resources: Not Available\n" +
		"Cluster is NVCF-Not-Ready\n"})
	assert.Equal(t, SeverityError, roleAware.Severity, "a role-aware image's failure stands")
	assert.NotContains(t, roleAware.Message, "predates")

	early := gradeWith(validatorRoleControlPlane, ClusterValidatorResult{ExitCode: 1,
		Logs: "level=fatal msg=\"Failed to create Kubernetes client\"\n"})
	assert.Equal(t, SeverityError, early.Severity)
	assert.NotContains(t, early.Message, "predates", "no GPU set, so nothing says the image is old")
}

// A Job that succeeded is a clean pass only when its transcript shows the
// requested checks reaching their verdict. Otherwise warnings may be hidden:
// the row warns, names why, says how to read the transcript, and --wait polls
// again.
func TestClusterValidatorCheck_PassNeedsTheRoleLineAndTheVerdict(t *testing.T) {
	for name, tc := range map[string]struct {
		role    string
		res     ClusterValidatorResult
		want    string
		cleanOK bool
	}{
		"empty transcript": {role: validatorRoleControlPlane, want: "its transcript was empty"},
		"unreadable": {role: validatorRoleControlPlane, res: ClusterValidatorResult{
			LogsErr: errors.New("connection refused to kubelet")}, want: "connection refused to kubelet"},
		"no role line": {role: validatorRoleControlPlane, res: ClusterValidatorResult{
			Logs: "Starting NVCF cluster validation\nCluster is NVCF-Ready\n"}, want: "does not show the control-plane checks"},
		"other role": {role: validatorRoleControlPlane, res: ClusterValidatorResult{
			Logs: "Validator role: compute-plane\nCluster is NVCF-Ready\n"}, want: "does not show the control-plane checks"},
		"cut off before the verdict": {role: validatorRoleControlPlane, res: ClusterValidatorResult{
			Logs: "Validator role: control-plane\nTier-1: ok\n"}, want: "the transcript has no verdict"},
		"ready":                {role: validatorRoleControlPlane, cleanOK: true},
		"legacy compute-plane": {role: validatorRoleComputePlane, cleanOK: true},
	} {
		tc.res.Passed, tc.res.JobName = true, "job-1"
		switch {
		case name == "ready":
			tc.res.Logs = "Validator role: control-plane\nCluster is NVCF-Ready\n"
		case name == "legacy compute-plane":
			tc.res.Logs = "=== GPU Resources ===\nCluster is NVCF-Ready\n"
		}
		r := gradeWith(tc.role, tc.res)
		if tc.cleanOK {
			assert.True(t, r.Passed, name)
			assert.Equal(t, SeverityInfo, r.Severity, name)
			continue
		}
		assert.False(t, r.Passed, name)
		assert.Equal(t, SeverityWarning, r.Severity, name)
		assert.True(t, r.Transient, "%s: --wait polls again", name)
		assert.Contains(t, r.Message, "verdict could not be read", name)
		assert.Contains(t, r.Message, tc.want, name)
		assert.Contains(t, r.Message, "kubectl --context ctx-a logs -n default job/job-1", name)
	}
}

// A failed run names the rows that failed it, unobserved critical checks
// included. One that ended before its verdict, killed or evicted, says so
// instead of blaming the cluster's checks.
func TestClusterValidatorCheck_FailuresNameTheirRows(t *testing.T) {
	failed := gradeWith(validatorRoleControlPlane, ClusterValidatorResult{ExitCode: 1, Logs: "Validator role: " +
		"control-plane\nCheck Results:\n\u2713    Control Plane: Healthy\n" +
		"\u2717    Tier-2 StatefulSets: nats-system/nats not ready\n" +
		"\u26a0    Gateway API: Status Unknown (check did not run)\n" +
		"\u26a0    Worker Nodes: 1 NotReady (non-blocking)\n" +
		"\u2717  Cluster is NVCF-Not-Ready  \u2717\n" +
		"\u2717  Your cluster does not meet all requirements for NVCF workloads\n"})
	assert.Equal(t, SeverityError, failed.Severity)
	assert.Equal(t, "cluster-validator reported failures: Tier-2 StatefulSets: nats-system/nats not ready; "+
		"Gateway API: Status Unknown (check did not run)", failed.Message)

	oom := gradeWith(validatorRoleControlPlane, ClusterValidatorResult{ExitCode: 137, Reason: "OOMKilled",
		Logs: "Validator role: control-plane\nTier-1: ok\n"})
	assert.Equal(t, SeverityError, oom.Severity)
	assert.Contains(t, oom.Message, "the validator exited 137 before printing a verdict (OOMKilled)")

	evicted := gradeWith(validatorRoleComputePlane, ClusterValidatorResult{ExitCode: -1, Reason: "Evicted"})
	assert.Contains(t, evicted.Message, "its pod ended before the validator printed a verdict (Evicted)")
}

// The Job's status can trail its pod's by more than the poll: the pod has
// succeeded while the Job still reads as running. The run waits for the Job,
// the authority, and grades on it.
func TestRunClusterValidator_JobStatusTrailingThePodIsGraded(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prevTimeout, prevGrace := clusterValidatorTimeout, validatorDeadlineGrace
	clusterValidatorTimeout, validatorDeadlineGrace = 300*time.Millisecond, 6*time.Second
	t.Cleanup(func() { clusterValidatorTimeout, validatorDeadlineGrace = prevTimeout, prevGrace })
	start := time.Now()
	client := lifecycleClient(func(name string) *batchv1.Job {
		if time.Since(start) > 2500*time.Millisecond {
			return succeeded(name)
		}
		return running(name)
	}, "")
	client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase {
		if time.Since(start) > 700*time.Millisecond {
			return corev1.PodSucceeded
		}
		return corev1.PodRunning
	}))

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
	assert.Empty(t, leftovers(t, client, res.RunID))
}

// Job reads that fail before the pod exists leave an empty pod list, which
// proves nothing: the pod may still be created. Its RBAC stays.
func TestRunClusterValidator_ReadOutageBeforeThePodExistsKeepsRBAC(t *testing.T) {
	prevPoll, prevTimeout := clusterValidatorPollInterval, clusterValidatorTimeout
	clusterValidatorPollInterval, clusterValidatorTimeout = 20*time.Millisecond, time.Second
	t.Cleanup(func() { clusterValidatorPollInterval, clusterValidatorTimeout = prevPoll, prevTimeout })
	client := lifecycleClient(running, "")
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
	})
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{}, nil
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.True(t, res.LeftBehind)
	assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1)
}

// Under --no-cleanup too, a validator that passed while its Job could not be
// read passes: the Job is read again once the wait ends.
func TestRunClusterValidator_NoCleanupReadOutageIsGradedOnTheJob(t *testing.T) {
	prev := clusterValidatorPollInterval
	clusterValidatorPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { clusterValidatorPollInterval = prev })
	client := lifecycleClient(succeeded, "")
	var failedReads atomic.Int32
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if failedReads.Add(1) <= int32(validatorGetErrorLimit) {
			return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
		}
		return false, nil, nil
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
}

// A pod being deleted may still be running its SIGTERM cleanup, and an empty
// pod list means the pods ended only once the Job will create none.
func TestValidatorPodsEnded(t *testing.T) {
	now := metav1.Now()
	pod := func(phase corev1.PodPhase, deleting bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p-" + string(phase), Namespace: clusterValidatorNamespace,
			Labels: map[string]string{"job-name": "j"}}, Status: corev1.PodStatus{Phase: phase}}
		if deleting {
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"batch.kubernetes.io/job-tracking"}
		}
		return p
	}
	job := func(mutate func(*batchv1.Job)) *batchv1.Job {
		j := running("j")
		mutate(j)
		return j
	}
	for name, tc := range map[string]struct {
		objects []runtime.Object
		want    bool
	}{
		"ended":                        {objects: []runtime.Object{pod(corev1.PodFailed, false)}, want: true},
		"evicted, still terminating":   {objects: []runtime.Object{pod(corev1.PodFailed, true)}},
		"running with a deletion mark": {objects: []runtime.Object{pod(corev1.PodRunning, true)}},
		"no pod, Job running":          {objects: []runtime.Object{running("j")}},
		"no pod, Job gone":             {want: true},
		"no pod, Job finished": {objects: []runtime.Object{job(func(j *batchv1.Job) {
			j.Status.Failed = 1
		})}, want: true},
		"no pod, Job being deleted": {objects: []runtime.Object{job(func(j *batchv1.Job) {
			j.DeletionTimestamp = &now
			j.Finalizers = []string{"foregroundDeletion"}
		})}, want: true},
	} {
		client := fake.NewSimpleClientset(tc.objects...)
		assert.Equal(t, tc.want, validatorPodsEnded(context.Background(), client, "j"), name)
	}
}

// An evicted or preempted pod counts as failed while it still runs its
// SIGTERM cleanup. Its RBAC stays until it ends, the result says how to
// remove it, and the row reads as the pod ending early, not failed checks.
func TestRunClusterValidator_EvictedPodKeepsItsRBAC(t *testing.T) {
	prev := validatorStopTimeout
	validatorStopTimeout = 300 * time.Millisecond
	t.Cleanup(func() { validatorStopTimeout = prev })
	client := lifecycleClient(func(name string) *batchv1.Job {
		j := running(name)
		j.Status.Failed = 1
		return j
	}, "")
	now := metav1.Now()
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		_, obj, err := podPhaseReactor(func() corev1.PodPhase { return corev1.PodRunning })(action)
		list := obj.(*corev1.PodList)
		list.Items[0].DeletionTimestamp = &now
		return true, list, err
	})
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "" {
			return false, nil, nil
		}
		return true, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: action.(ktesting.GetAction).GetName(), Namespace: clusterValidatorNamespace},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, Reason: "Evicted"},
		}, nil
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.False(t, res.Passed)
	assert.True(t, res.LeftBehind)
	assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1)
	assert.Equal(t, int32(-1), res.ExitCode)
	assert.Equal(t, "Evicted", res.Reason)
}

// The wait for a Job to finish retries failed reads, and ends at once when the
// Job and its pods are gone.
func TestAwaitValidatorJob(t *testing.T) {
	client := fake.NewSimpleClientset(succeeded("j"))
	reads := 0
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if reads++; reads <= 2 {
			return true, nil, apierrors.NewInternalError(errors.New("etcd timeout"))
		}
		return false, nil, nil
	})
	job, podsDone, ended := awaitValidatorJob(context.Background(), client, "j", 10*time.Second)
	require.NotNil(t, job, "a failed read is retried")
	assert.True(t, podsDone)
	assert.False(t, ended)

	start := time.Now()
	job, podsDone, _ = awaitValidatorJob(context.Background(), fake.NewSimpleClientset(), "j", 10*time.Second)
	assert.Nil(t, job)
	assert.True(t, podsDone)
	assert.Less(t, time.Since(start), time.Second, "a Job that is gone with its pods is not waited on")
}

// The Job's active deadline counts from its create, so it is derived from
// what is left of the run's own timeout then. It ends the pod after that
// timeout, and with the pod's termination grace inside the wait for it.
func TestRunClusterValidator_ActiveDeadlineFollowsTheRunsTimeout(t *testing.T) {
	assert.Positive(t, validatorDeadlineOffset, "the deadline must not cut short the run's own wait")
	assert.LessOrEqual(t, validatorDeadlineOffset+clusterValidatorTerminationGrace, defaultValidatorDeadlineGrace,
		"the pod must be gone before the CLI stops waiting for it")
	assert.Equal(t, int64(68), validatorActiveDeadlineSeconds(7200*time.Millisecond))

	prev := clusterValidatorTimeout
	clusterValidatorTimeout = 10 * time.Second
	t.Cleanup(func() { clusterValidatorTimeout = prev })
	client := lifecycleClient(succeeded, "")
	// A slow apiserver spends part of the timeout before the Job exists.
	client.PrependReactor("create", "serviceaccounts", func(ktesting.Action) (bool, runtime.Object, error) {
		time.Sleep(2 * time.Second)
		return false, nil, nil
	})
	var deadline int64
	client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
		deadline = *a.(ktesting.CreateAction).GetObject().(*batchv1.Job).Spec.ActiveDeadlineSeconds
		return false, nil, nil
	})
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.LessOrEqual(t, deadline, int64(8+60), "the deadline counts from the create, not the start of the run")
	assert.Greater(t, deadline, int64(60))
}

// Each term of the run ceiling, with its multiplier, pinned against a sum
// written out here: a term dropped from the formula leaves the budget short.
func TestClusterValidatorRunCeiling_Terms(t *testing.T) {
	prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin := clusterValidatorTimeout,
		validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout, validatorRunMargin
	t.Cleanup(func() {
		clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
			validatorRunMargin = prevTimeout, prevGrace, prevLogs, prevCleanup, prevMargin
	})
	clusterValidatorTimeout, validatorDeadlineGrace, clusterValidatorLogFetchTimeout, validatorCleanupTimeout,
		validatorRunMargin = time.Second, 10*time.Second, 100*time.Second, 1000*time.Second, 10000*time.Second
	assert.Equal(t, (1+10+4*100+3*1000+10000)*time.Second, ClusterValidatorRunCeiling())
}

// logsClient is a fake clientset whose pod logs come from a real client, so a
// test can change a transcript between reads.
type logsClient struct {
	*fake.Clientset
	logs kubernetes.Interface
}

func (c logsClient) CoreV1() typedcorev1.CoreV1Interface {
	return logsCoreV1{CoreV1Interface: c.Clientset.CoreV1(), logs: c.logs}
}

type logsCoreV1 struct {
	typedcorev1.CoreV1Interface
	logs kubernetes.Interface
}

func (c logsCoreV1) Pods(ns string) typedcorev1.PodInterface {
	return logsPods{PodInterface: c.CoreV1Interface.Pods(ns), logs: c.logs.CoreV1().Pods(ns)}
}

type logsPods struct {
	typedcorev1.PodInterface
	logs typedcorev1.PodInterface
}

func (p logsPods) GetLogs(name string, opts *corev1.PodLogOptions) *rest.Request {
	return p.logs.GetLogs(name, opts)
}

// withLogs serves every pod log read on client from transcript().
func withLogs(t *testing.T, client *fake.Clientset, transcript func() string) kubernetes.Interface {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, transcript())
	}))
	t.Cleanup(srv.Close)
	logs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)
	return logsClient{Clientset: client, logs: logs}
}

// A validator that finishes after the CLI's own timeout is graded on its
// whole transcript, read again once it has finished: the partial one read at
// the timeout holds neither its warnings nor its verdict.
func TestRunClusterValidator_LateFinishRereadsTheTranscript(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prevTimeout, prevGrace := clusterValidatorTimeout, validatorDeadlineGrace
	clusterValidatorTimeout, validatorDeadlineGrace = 300*time.Millisecond, 5*time.Second
	t.Cleanup(func() { clusterValidatorTimeout, validatorDeadlineGrace = prevTimeout, prevGrace })
	var finished atomic.Bool
	fakeClient := lifecycleClient(func(name string) *batchv1.Job {
		if finished.Load() {
			return succeeded(name)
		}
		return running(name)
	}, "")
	client := withLogs(t, fakeClient, func() string {
		if finished.Load() {
			return "Validator role: control-plane\nnats: rolling update in progress\n" +
				"Cluster is NVCF-Ready (with warnings)\n"
		}
		return "Validator role: control-plane\n"
	})
	timer := time.AfterFunc(time.Second, func() { finished.Store(true) })
	t.Cleanup(func() { timer.Stop() })

	res := runValidatorJob(context.Background(), client, ClusterValidatorParams{
		Image: "nvcr.io/nvidia/validator:1", Role: clusterValidatorControlPlaneRole,
	})
	require.NoError(t, res.Err)
	assert.Contains(t, res.Logs, validatorReadyWithWarnings)
	r := gradeWith(validatorRoleControlPlane, res)
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.True(t, r.Transient, "the rollout keeps --wait polling")
}

// A check's own timeout under a live budget is its result, not the budget
// cutting it short: exit 2, not 5.
func TestRunPreflight_ACheckOwnTimeoutIsNotCutShort(t *testing.T) {
	cv := func(context.Context, ClusterValidatorParams) ClusterValidatorResult {
		return ClusterValidatorResult{Err: fmt.Errorf("reading the Job: %w", context.DeadlineExceeded)}
	}
	results := RunPreflightForRole(context.Background(), PreflightConfig{}, RoleControlPlane,
		RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1"}, &noopSink{})
	require.Len(t, results, 1)
	assert.False(t, results[0].CutShort)
	assert.True(t, results[0].IsBlockingFailure())
}

// A pull failure that clears restarts the grace: only a failure that lasts
// the whole grace is final. The production grace covers the kubelet's second
// pull retry.
func TestWaitForClusterValidatorJob_PullFailureThatClearsRestartsTheGrace(t *testing.T) {
	assert.GreaterOrEqual(t, defaultValidatorPullFailureGrace, 60*time.Second)
	prevGrace, prevPoll := validatorPullFailureGrace, clusterValidatorPollInterval
	validatorPullFailureGrace, clusterValidatorPollInterval = 200*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { validatorPullFailureGrace, clusterValidatorPollInterval = prevGrace, prevPoll })
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, running("j"), nil
	})
	client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodPending }))
	start := time.Now()
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: action.(ktesting.GetAction).GetName(),
			Namespace: clusterValidatorNamespace}}
		// Failing, then pulled for a moment, then failing again.
		if since := time.Since(start); since < 120*time.Millisecond || since > 180*time.Millisecond {
			pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: clusterValidatorContainer,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull",
					Message: "503 Service Unavailable"}}}}
		}
		return true, pod, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 340*time.Millisecond)
	defer cancel()
	_, err := waitForClusterValidatorJob(ctx, client, "j")
	var pullErr *validatorImagePullError
	assert.False(t, errors.As(err, &pullErr), "no single failure lasted the grace")
}

// The removal command is safe to paste at any time: it looks at the pod,
// stops the Job in the foreground so the pod ends while it still has its
// RBAC, revokes the cluster-wide RBAC even if a namespaced delete fails, then
// deletes the rest. It names the context the run used.
func TestValidatorRemovalCommand(t *testing.T) {
	sel := clusterValidatorRunLabel + "=r1"
	assert.Equal(t, "kubectl --context ctx-a get pods -n default -l "+sel+"; "+
		"kubectl --context ctx-a delete -n default job -l "+sel+" --cascade=foreground --wait; "+
		"kubectl --context ctx-a delete clusterrolebinding,clusterrole -l "+sel+"; "+
		"kubectl --context ctx-a delete -n default rolebinding,role,serviceaccount,secret,configmap -l "+sel,
		validatorRemovalCommand("ctx-a", "r1"))
}

// In a single-cluster run no context is passed, so every hint names the
// current context the run used: pasted after a context switch, it still
// reaches that cluster.
func TestClusterValidatorCheck_HintsPinTheCurrentContext(t *testing.T) {
	prev := currentKubeContextNameFn
	currentKubeContextNameFn = func() string { return "kind-prod" }
	t.Cleanup(func() { currentKubeContextNameFn = prev })
	var got ClusterValidatorParams
	cv := func(_ context.Context, p ClusterValidatorParams) ClusterValidatorResult {
		got = p
		return ClusterValidatorResult{JobName: "job-1", RunID: "r1", Created: true, Err: errors.New("boom")}
	}
	r := clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1",
		ClusterValidatorNoCleanup: true}, validatorRoleControlPlane).Run(context.Background())
	assert.Equal(t, "kind-prod", got.KubeContext, "the run uses the context its hints name")
	assert.Equal(t, validatorRemovalCommand("kind-prod", "r1"), r.Cleanup)
	assert.Contains(t, r.Detail, "logs: kubectl --context kind-prod logs -n default job/job-1")
	assert.Contains(t, r.Detail, "kept with --no-cleanup; remove with: "+r.Cleanup)
}

// The removal command is printed whenever the run kept something, and only
// then: --no-cleanup with objects created, a pod that may still run, or a
// sweep that failed. Every run's command goes to the ledger as it starts.
func TestClusterValidatorCheck_CleanupRow(t *testing.T) {
	for name, tc := range map[string]struct {
		noCleanup bool
		res       ClusterValidatorResult
		why       string
	}{
		"no-cleanup pass":           {noCleanup: true, res: ClusterValidatorResult{Created: true, Passed: true}, why: "kept with --no-cleanup"},
		"no-cleanup interrupted":    {noCleanup: true, res: ClusterValidatorResult{Created: true, Err: context.Canceled}, why: "kept with --no-cleanup"},
		"no-cleanup, nothing made":  {noCleanup: true, res: ClusterValidatorResult{Err: errors.New("create service account: forbidden")}},
		"no-cleanup bootstrap fail": {noCleanup: true, res: ClusterValidatorResult{Created: true, Err: errors.New("create cluster role: forbidden")}, why: "kept with --no-cleanup"},
		"pod may run":               {res: ClusterValidatorResult{Created: true, LeftBehind: true, Err: errors.New("x")}, why: "may still be running"},
		"sweep failed": {res: ClusterValidatorResult{Created: true, Passed: true, SweepErr: errors.New("503 from apiserver")},
			why: "removing the run's objects failed (503 from apiserver)"},
		"clean": {res: ClusterValidatorResult{Created: true, Passed: true}},
	} {
		ledger := &CleanupLedger{}
		var during []string
		cv := func(_ context.Context, p ClusterValidatorParams) ClusterValidatorResult {
			p.OnStart("r1")
			during = ledger.Outstanding()
			res := tc.res
			res.RunID = "r1"
			return res
		}
		r := clusterValidatorCheck(RoleConfig{ClusterValidator: cv, ClusterValidatorImage: "img:1", KubeContext: "ctx-a",
			ClusterValidatorNoCleanup: tc.noCleanup, ValidatorCleanup: ledger}, validatorRoleComputePlane).Run(context.Background())
		cmd := validatorRemovalCommand("ctx-a", "r1")
		assert.Equal(t, []string{cmd}, during, "%s: registered before the run creates anything", name)
		if tc.why == "" {
			assert.Empty(t, r.Cleanup, name)
			assert.Empty(t, ledger.Outstanding(), name)
			continue
		}
		assert.Equal(t, cmd, r.Cleanup, name)
		assert.Contains(t, r.Detail, tc.why, name)
		assert.Equal(t, []string{cmd}, ledger.Outstanding(), name)
	}
}

// A sweep that fails leaves the run's objects, so the result says so and the
// row carries the removal command.
func TestRunClusterValidator_FailedSweepIsReported(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")
	client.PrependReactor("delete", "clusterroles", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
	})
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.NoError(t, res.Err)
	require.Error(t, res.SweepErr)
	assert.Contains(t, res.SweepErr.Error(), "the apiserver is restarting")
	assert.Len(t, leftovers(t, client, res.RunID)["ClusterRole"], 1)
}

// Under --no-cleanup a kept Job whose pod cannot pull is suspended on every
// path, so the pod stops retrying: here the run is interrupted inside the
// pull grace. Its row points at the Job, not at logs its deleted pod never had.
func TestRunClusterValidator_NoCleanupSuspendsAPullingJobOnInterrupt(t *testing.T) {
	prev := validatorPullFailureGrace
	validatorPullFailureGrace = time.Hour
	t.Cleanup(func() { validatorPullFailureGrace = prev })
	client := lifecycleClient(running, "ImagePullBackOff")
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(500*time.Millisecond, cancel)
	t.Cleanup(func() { timer.Stop() })

	res := runClusterValidator(ctx, client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.ErrorIs(t, res.Err, context.Canceled)
	assert.True(t, jobSuspended(client))
	assert.True(t, res.Suspended)
	assert.Empty(t, jobDeletes(client))
}

// jobSuspended reports whether the client saw the Job suspended.
func jobSuspended(client *fake.Clientset) bool {
	for _, a := range client.Actions() {
		if p, ok := a.(ktesting.PatchAction); ok && p.GetResource().Resource == "jobs" &&
			strings.Contains(string(p.GetPatch()), `"suspend":true`) {
			return true
		}
	}
	return false
}

// A kept, unpullable Job is suspended, so its pod is deleted: the row says to
// describe the Job, not to read logs that never existed. A suspend that fails
// leaves the pod retrying, and the row says so.
func TestRunClusterValidator_NoCleanupPullFailure(t *testing.T) {
	client := lifecycleClient(running, "ImagePullBackOff")
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	var pullErr *validatorImagePullError
	require.ErrorAs(t, res.Err, &pullErr)
	require.True(t, res.Suspended)
	assert.Equal(t, "inspect: kubectl --context ctx-a describe -n default job/"+res.JobName,
		clusterValidatorDetail("ctx-a", res))

	denied := lifecycleClient(running, "ImagePullBackOff")
	denied.PrependReactor("patch", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: batchv1.GroupName, Resource: "jobs"},
			"j", errors.New("cannot patch jobs"))
	})
	res = runClusterValidator(context.Background(), denied, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.ErrorAs(t, res.Err, &pullErr)
	assert.False(t, res.Suspended)
	assert.Contains(t, res.Err.Error(), "suspending the kept Job failed, so its pod keeps retrying the pull")
	assert.Contains(t, res.Err.Error(), "cannot patch jobs")
}

// A create that fails under --no-cleanup may still have been applied: the
// kept Job is suspended, since nothing follows it and with no deadline a pod
// that cannot pull would retry forever.
func TestRunClusterValidator_NoCleanupFailedCreateIsSuspended(t *testing.T) {
	client := lifecycleClient(running, "")
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		require.NoError(t, client.Tracker().Add(action.(ktesting.CreateAction).GetObject()))
		return true, nil, apierrors.NewInternalError(errors.New("connection reset by peer"))
	})
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.ErrorContains(t, res.Err, "creating validator Job")
	assert.True(t, jobSuspended(client))
	assert.Empty(t, jobDeletes(client))
}

// ctxPatchClient fails a Secret or ConfigMap patch whose context has ended, as
// a real client does; the fake clientset ignores contexts.
type ctxPatchClient struct{ *fake.Clientset }

func (c ctxPatchClient) CoreV1() typedcorev1.CoreV1Interface {
	return ctxPatchCoreV1{CoreV1Interface: c.Clientset.CoreV1()}
}

type ctxPatchCoreV1 struct{ typedcorev1.CoreV1Interface }

func (c ctxPatchCoreV1) Secrets(ns string) typedcorev1.SecretInterface {
	return ctxPatchSecrets{SecretInterface: c.CoreV1Interface.Secrets(ns)}
}

func (c ctxPatchCoreV1) ConfigMaps(ns string) typedcorev1.ConfigMapInterface {
	return ctxPatchConfigMaps{ConfigMapInterface: c.CoreV1Interface.ConfigMaps(ns)}
}

type ctxPatchSecrets struct{ typedcorev1.SecretInterface }

func (s ctxPatchSecrets) Patch(ctx context.Context, name string, pt types.PatchType, data []byte,
	opts metav1.PatchOptions, sub ...string) (*corev1.Secret, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.SecretInterface.Patch(ctx, name, pt, data, opts, sub...)
}

type ctxPatchConfigMaps struct{ typedcorev1.ConfigMapInterface }

func (c ctxPatchConfigMaps) Patch(ctx context.Context, name string, pt types.PatchType, data []byte,
	opts metav1.PatchOptions, sub ...string) (*corev1.ConfigMap, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.ConfigMapInterface.Patch(ctx, name, pt, data, opts, sub...)
}

// An interrupt right after the Job create must not leave the NGC-key Secret
// and the ConfigMap unowned: if the stopped pod does not end, the Job's TTL is
// then the only thing that removes them with it.
func TestRunClusterValidator_OwnershipSurvivesAnInterruptAfterTheCreate(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prev := validatorStopTimeout
	validatorStopTimeout = 200 * time.Millisecond
	t.Cleanup(func() { validatorStopTimeout = prev })
	fakeClient := lifecycleClient(running, "")
	fakeClient.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodRunning }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fakeClient.PrependReactor("create", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return false, nil, nil
	})

	res := runValidatorJob(ctx, ctxPatchClient{fakeClient}, ClusterValidatorParams{
		Image: "nvcr.io/nvidia/validator:1", Role: clusterValidatorControlPlaneRole,
	})
	require.ErrorIs(t, res.Err, context.Canceled)
	require.True(t, res.LeftBehind)
	left := leftovers(t, fakeClient, res.RunID)
	require.Len(t, left["Secret"], 1)
	require.Len(t, left["ConfigMap"], 1)
	sec, err := fakeClient.CoreV1().Secrets(clusterValidatorNamespace).Get(ctx, left["Secret"][0], metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, sec.OwnerReferences, 1)
	cm, err := fakeClient.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(ctx, left["ConfigMap"][0], metav1.GetOptions{})
	require.NoError(t, err)
	assert.Len(t, cm.OwnerReferences, 1)
}

// An outage early in the run ends the wait early, but the validator still has
// the rest of its timeout: the wait for its result covers that as well as the
// deadline grace, so a validator that passes late in its timeout passes.
func TestRunClusterValidator_EarlyOutageThenLatePass(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	prevPoll, prevTimeout := clusterValidatorPollInterval, clusterValidatorTimeout
	clusterValidatorPollInterval, clusterValidatorTimeout = 20*time.Millisecond, 3*time.Second
	t.Cleanup(func() { clusterValidatorPollInterval, clusterValidatorTimeout = prevPoll, prevTimeout })
	start := time.Now()
	late := func() bool { return time.Since(start) > validatorDeadlineGrace+500*time.Millisecond }
	client := lifecycleClient(func(name string) *batchv1.Job {
		if late() {
			return succeeded(name)
		}
		return running(name)
	}, "")
	var failedReads atomic.Int32
	client.PrependReactor("get", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		if failedReads.Add(1) <= int32(validatorGetErrorLimit) {
			return true, nil, apierrors.NewServiceUnavailable("the apiserver is restarting")
		}
		return false, nil, nil
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
	assert.False(t, res.LeftBehind)
}

// A pull Secret the run could not create is named when the pull then fails,
// rather than leaving only the registry's "unauthorized" to blame the key.
func TestRunClusterValidator_PullFailureNamesTheRefusedPullSecret(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "ImagePullBackOff")
	client.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("secrets"), "x", errors.New("denied by policy"))
	})

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, nil, nil)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "cannot pull image")
	assert.Contains(t, res.Err.Error(), "the run's pull secret was not created")
	assert.Contains(t, res.Err.Error(), "denied by policy")
}

// A credential copied into the validator namespace is reported on the
// validator's row, with where it came from.
func TestClusterValidatorCheck_ReportsACopiedPullCredential(t *testing.T) {
	client := lifecycleClient(succeeded, "")
	cfg, err := buildDockerConfigJSON("nvcr.io", "$oauthtoken", "k")
	require.NoError(t, err)
	_, err = client.CoreV1().Secrets("vault-system").Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "regcred", Namespace: "vault-system"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: cfg},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.Len(t, res.Notes, 1)
	assert.Contains(t, res.Notes[0], "copied the pull credential for nvcr.io from vault-system/regcred into default/")

	r := clusterValidatorCheck(RoleConfig{
		ClusterValidator:      func(context.Context, ClusterValidatorParams) ClusterValidatorResult { return res },
		ClusterValidatorImage: "nvcr.io/nvidia/validator:1", KubeContext: "ctx",
	}, clusterValidatorComputePlaneRole).Run(context.Background())
	assert.Contains(t, r.Detail, "copied the pull credential for nvcr.io from vault-system/regcred")
}
