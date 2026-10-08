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
	"strings"
	"sync"
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

// stampCreates makes every Create return what an apiserver does: a UID, and a
// creationTimestamp on the apiserver's clock, which runs skew from the local
// one. It records each created object's UID by "<resource>/<name>".
func stampCreates(client *fake.Clientset, skew time.Duration) *sync.Map {
	uids := &sync.Map{}
	client.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		o, ok := a.(ktesting.CreateAction).GetObject().(metav1.Object)
		if !ok {
			return false, nil, nil
		}
		uid := types.UID("uid-" + a.GetResource().Resource + "-" + o.GetName())
		o.SetUID(uid)
		o.SetCreationTimestamp(metav1.NewTime(time.Now().Add(skew)))
		uids.Store(a.GetResource().Resource+"/"+o.GetName(), uid)
		return false, nil, nil
	})
	return uids
}

// jobEnv returns the env of the Job the run created.
func jobEnv(t *testing.T, client *fake.Clientset) map[string]string {
	t.Helper()
	for _, a := range client.Actions() {
		if c, ok := a.(ktesting.CreateAction); ok && a.GetResource().Resource == "jobs" {
			env := map[string]string{}
			for _, e := range c.GetObject().(*batchv1.Job).Spec.Template.Spec.Containers[0].Env {
				env[e.Name] = e.Value
			}
			return env
		}
	}
	t.Fatal("the run created no Job")
	return nil
}

// Every delete a run makes is pinned to the UID its Create returned and to
// nothing else. A ResourceVersion precondition turns every delete of a Job,
// whose status its controller keeps writing, into a 409 the sweeps swallow.
func TestRunClusterValidator_DeletesArePinnedToTheCreatedUID(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(running, "ImagePullBackOff")
	uids := stampCreates(client, 0)

	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", false, clusterValidatorControlPlaneRole, []RegistryEntry{{Registry: "nvcr.io"}}, nil)
	require.Error(t, res.Err)

	deleted := map[string]bool{}
	for _, a := range client.Actions() {
		d, ok := a.(ktesting.DeleteActionImpl)
		if !ok {
			continue
		}
		key := d.GetResource().Resource + "/" + d.Name
		want, created := uids.Load(key)
		require.True(t, created, "%s was deleted but not created by this run", key)
		require.NotNil(t, d.DeleteOptions.Preconditions, key)
		require.NotNil(t, d.DeleteOptions.Preconditions.UID, key)
		assert.Equal(t, want, *d.DeleteOptions.Preconditions.UID, key)
		assert.Nil(t, d.DeleteOptions.Preconditions.ResourceVersion, key)
		deleted[d.GetResource().Resource] = true
	}
	for _, r := range []string{
		"jobs", "serviceaccounts", "clusterroles", "clusterrolebindings", "roles", "rolebindings", "secrets",
		"configmaps",
	} {
		assert.True(t, deleted[r], "a pull failure removes the run's %s", r)
	}
}

// The Job is named per role and run, like the RBAC. A name from the clock
// alone collides across the two concurrent roles where the clock is coarse,
// and the loser then stopped the winner's live Job.
func TestRunClusterValidator_JobIsNamedPerRoleAndRun(t *testing.T) {
	for _, role := range []string{clusterValidatorControlPlaneRole, clusterValidatorComputePlaneRole} {
		client := lifecycleClient(succeeded, "")
		res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
			"", false, role, nil, nil)
		require.NoError(t, res.Err)
		assert.Equal(t, clusterValidatorRBACName(role, res.RunID), res.JobName)
	}
}

// Another run's Job under this run's name is never stopped: not on
// AlreadyExists, which proves the name was taken, and not when a Create
// errored in a way that may have been applied, unless the Job read back
// carries this run's ID.
func TestRunClusterValidator_AnotherRunsJobUnderThisRunsNameSurvives(t *testing.T) {
	for name, createErr := range map[string]func(string) error{
		"already exists": func(n string) error {
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: "batch", Resource: "jobs"}, n)
		},
		"connection reset": func(string) error { return apierrors.NewInternalError(errors.New("connection reset")) },
	} {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
				job := a.(ktesting.CreateAction).GetObject().(*batchv1.Job)
				other := job.DeepCopy()
				other.Labels = clusterValidatorRunLabels(clusterValidatorComputePlaneRole, "otherrun", false)
				other.UID = "uid-other"
				require.NoError(t, client.Tracker().Add(other))
				return true, nil, createErr(job.Name)
			})

			// The other run's pod is still running.
			client.PrependReactor("list", "pods", podPhaseReactor(func() corev1.PodPhase { return corev1.PodRunning }))

			res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
				"", false, clusterValidatorComputePlaneRole, nil, nil)
			require.ErrorContains(t, res.Err, "creating validator Job")
			assert.Empty(t, jobDeletes(client), "another run's Job must not be stopped")
			assert.False(t, res.LeftBehind, "another run's pod does not keep this run's objects")
			assert.Empty(t, leftovers(t, client, res.RunID))
			_, err := client.BatchV1().Jobs(clusterValidatorNamespace).Get(context.Background(),
				clusterValidatorRBACName(clusterValidatorComputePlaneRole, res.RunID), metav1.GetOptions{})
			assert.NoError(t, err)
		})
		// A kept run suspends its own Job when the create errored, never
		// another run's under the name.
		t.Run(name+", --no-cleanup", func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "jobs", func(a ktesting.Action) (bool, runtime.Object, error) {
				job := a.(ktesting.CreateAction).GetObject().(*batchv1.Job)
				other := job.DeepCopy()
				other.Labels = clusterValidatorRunLabels(clusterValidatorComputePlaneRole, "otherrun", false)
				other.UID = "uid-other"
				require.NoError(t, client.Tracker().Add(other))
				return true, nil, createErr(job.Name)
			})
			res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
				"", true, clusterValidatorComputePlaneRole, nil, nil)
			require.ErrorContains(t, res.Err, "creating validator Job")
			assert.False(t, jobSuspended(client), "another run's Job must not be suspended")
			assert.NotContains(t, res.Err.Error(), "suspending the kept Job failed")
		})
	}
}

// A kept Job is suspended with its UID in the patch, so the apiserver refuses
// it if another object has taken the name since.
func TestSuspendValidatorJob_IsPinnedToTheJobsUID(t *testing.T) {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "j", Namespace: clusterValidatorNamespace, UID: "uid-mine"}}
	client := fake.NewSimpleClientset(job)
	require.NoError(t, suspendValidatorJob(client, "j", "run1", runObjects{kindJob: job}))
	var patch string
	for _, a := range client.Actions() {
		if p, ok := a.(ktesting.PatchAction); ok && p.GetResource().Resource == "jobs" {
			patch = string(p.GetPatch())
		}
	}
	assert.Contains(t, patch, `"uid":"uid-mine"`)
	assert.Contains(t, patch, `"suspend":true`)
}

// A bootstrap that hits AlreadyExists did not create that object, so the
// cleanup must not delete it, whatever labels it wears. Only what the run
// created goes.
func TestRunClusterValidator_RefusedBootstrapObjectSurvivesTheCleanup(t *testing.T) {
	for name, lbls := range map[string]func(runID string) map[string]string{
		"unlabelled": func(string) map[string]string { return nil },
		"forged labels": func(runID string) map[string]string {
			return clusterValidatorRunLabels(clusterValidatorComputePlaneRole, runID, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			var runID string
			client.PrependReactor("create", "clusterroles", func(a ktesting.Action) (bool, runtime.Object, error) {
				cr := a.(ktesting.CreateAction).GetObject().(*rbacv1.ClusterRole)
				runID = cr.Labels[clusterValidatorRunLabel]
				squat := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: cr.Name, Labels: lbls(runID)}}
				require.NoError(t, client.Tracker().Add(squat))
				return true, nil, apierrors.NewAlreadyExists(
					schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}, cr.Name)
			})

			res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
				"", false, clusterValidatorComputePlaneRole, nil, nil)
			require.ErrorContains(t, res.Err, "bootstrapping validator RBAC")
			name := clusterValidatorRBACName(clusterValidatorComputePlaneRole, runID)
			_, err := client.RbacV1().ClusterRoles().Get(context.Background(), name, metav1.GetOptions{})
			assert.NoError(t, err, "the ClusterRole this run did not create must survive")
			_, err = client.CoreV1().ServiceAccounts(clusterValidatorNamespace).Get(context.Background(), name,
				metav1.GetOptions{})
			assert.True(t, apierrors.IsNotFound(err), "the ServiceAccount this run created is removed")
		})
	}
}

// A ConfigMap that appears under the run's name between the RBAC and the
// ConfigMap create, unlabelled or wearing forged managed labels, is neither
// adopted, owned, deleted nor read: the Job reads no ConfigMap at all.
func TestRunClusterValidator_PreExistingConfigMapIsNotAdoptedOrWired(t *testing.T) {
	for name, lbls := range map[string]func(runID string) map[string]string{
		"unlabelled": func(string) map[string]string { return nil },
		"forged labels": func(runID string) map[string]string {
			return clusterValidatorRunLabels(clusterValidatorControlPlaneRole, runID, false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := lifecycleClient(succeeded, "")
			var cmName string
			client.PrependReactor("create", "serviceaccounts", func(a ktesting.Action) (bool, runtime.Object, error) {
				runID := a.(ktesting.CreateAction).GetObject().(*corev1.ServiceAccount).Labels[clusterValidatorRunLabel]
				cmName = clusterValidatorConfigRunName(runID)
				require.NoError(t, client.Tracker().Add(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: clusterValidatorNamespace,
						Labels: lbls(runID)},
					Data: map[string]string{"config.yaml": "enforcement:\n  enabled: true\n"},
				}))
				return false, nil, nil
			})

			res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
				"", false, clusterValidatorControlPlaneRole, []RegistryEntry{{Registry: "nvcr.io"}}, nil)
			require.NoError(t, res.Err)
			assert.Contains(t, res.Logs, "validator config not applied")
			assert.Equal(t, clusterValidatorNoConfigName, jobEnv(t, client)["VALIDATOR_CONFIG_NAME"],
				"a ConfigMap this run did not create is never wired into the Job")

			cm, err := client.CoreV1().ConfigMaps(clusterValidatorNamespace).Get(context.Background(), cmName,
				metav1.GetOptions{})
			require.NoError(t, err, "the other ConfigMap must survive the run's cleanup")
			assert.Equal(t, "enforcement:\n  enabled: true\n", cm.Data["config.yaml"])
			assert.Empty(t, cm.OwnerReferences)
			for _, a := range client.Actions() {
				if a.GetResource().Resource == "configmaps" {
					assert.NotContains(t, []string{"update", "patch", "delete"}, a.GetVerb())
				}
			}
		})
	}
}

// Only the control-plane role reads a ConfigMap, and only one its run created.
// The compute role is pointed at a name no ConfigMap can have, never an empty
// value, which would let the validator fall back to its default name.
func TestRunClusterValidator_ConfigNamePerRole(t *testing.T) {
	cp := lifecycleClient(succeeded, "")
	res := runClusterValidator(context.Background(), cp, "registry.example.com/validator:1",
		"", false, clusterValidatorControlPlaneRole, []RegistryEntry{{Registry: "nvcr.io"}}, nil)
	require.NoError(t, res.Err)
	assert.Equal(t, clusterValidatorConfigRunName(res.RunID), jobEnv(t, cp)["VALIDATOR_CONFIG_NAME"])

	gpu := lifecycleClient(succeeded, "")
	res = runClusterValidator(context.Background(), gpu, "registry.example.com/validator:1",
		"", false, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)
	assert.Equal(t, clusterValidatorNoConfigName, jobEnv(t, gpu)["VALIDATOR_CONFIG_NAME"])
}

// Every object a run creates carries its role, run and managed labels, the
// ConfigMap included, so the removal command and the per-role selectors see
// all of them.
func TestRunClusterValidator_EveryObjectCarriesRoleAndRunLabels(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := lifecycleClient(succeeded, "")
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorControlPlaneRole, []RegistryEntry{{Registry: "nvcr.io"}}, nil)
	require.NoError(t, res.Err)
	seen := 0
	for _, a := range client.Actions() {
		c, ok := a.(ktesting.CreateAction)
		if !ok {
			continue
		}
		o := c.GetObject().(metav1.Object)
		assert.Equal(t, clusterValidatorControlPlaneRole, o.GetLabels()[clusterValidatorRoleLabel], o.GetName())
		assert.Equal(t, res.RunID, o.GetLabels()[clusterValidatorRunLabel], o.GetName())
		assert.True(t, hasValidatorManagedLabels(o.GetLabels()), o.GetName())
		seen++
	}
	assert.Equal(t, 8, seen, "RBAC (5), Secret, ConfigMap and Job")
}

// The NGC-key pull Secret is minted only once the run can start a Job. A
// kubeconfig that cannot create the RBAC never gets the key written into the
// namespace.
func TestRunClusterValidator_DeniedBootstrapMintsNoPullSecret(t *testing.T) {
	t.Setenv("NGC_API_KEY", "key")
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "clusterroles", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: rbacv1.GroupName, Resource: "clusterroles"}, "", nil)
	})
	res := runClusterValidator(context.Background(), client, "nvcr.io/nvidia/validator:1",
		"", true, clusterValidatorComputePlaneRole, nil, nil)
	require.Error(t, res.Err)
	for _, a := range client.Actions() {
		assert.False(t, a.GetVerb() == "create" && a.GetResource().Resource == "secrets",
			"no Secret may be created before the bootstrap succeeds")
	}
}

// The orphan sweep ages objects on the apiserver's clock. Here it runs ten
// hours behind the local clock, by which every object would look stale: only
// another run's object past the TTL on the apiserver's clock goes. The run's
// own objects and a Secret the operator named explicitly are never taken,
// however old, and the sweep runs before the Job is created.
func TestRunClusterValidator_OrphanSweepUsesTheServerClock(t *testing.T) {
	const skew = -10 * time.Hour
	serverAgo := func(age time.Duration) metav1.Time { return metav1.NewTime(time.Now().Add(skew - age)) }
	cr := func(run string, age time.Duration) *rbacv1.ClusterRole {
		return &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{
			Name:              clusterValidatorRBACName(clusterValidatorComputePlaneRole, run),
			Labels:            clusterValidatorRunLabels(clusterValidatorComputePlaneRole, run, false),
			CreationTimestamp: serverAgo(age),
		}}
	}
	stale, live := cr("stalerun", orphanValidatorRBACTTL+time.Minute), cr("liverun", orphanValidatorRBACTTL-time.Minute)
	explicit := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      validatorPullSecretRunName(clusterValidatorComputePlaneRole, "keptrun"),
		Namespace: clusterValidatorNamespace,
		Labels:    clusterValidatorRunLabels(clusterValidatorComputePlaneRole, "keptrun", true),
		// Past even the preserved TTL.
		CreationTimestamp: serverAgo(preservedValidatorTTL + time.Hour),
	}}
	client := lifecycleClient(succeeded, "")
	for _, o := range []runtime.Object{stale, live, explicit} {
		require.NoError(t, client.Tracker().Add(o))
	}
	// This run's ClusterRole reads as past even the preserved TTL: only the
	// run ID spares it. Registered first, so it runs after stampCreates.
	client.PrependReactor("create", "clusterroles", func(a ktesting.Action) (bool, runtime.Object, error) {
		a.(ktesting.CreateAction).GetObject().(metav1.Object).SetCreationTimestamp(
			serverAgo(preservedValidatorTTL + time.Hour))
		return false, nil, nil
	})
	stampCreates(client, skew)

	res := runClusterValidator(context.Background(), client, "registry.example.com/validator:1",
		explicit.Name, true, clusterValidatorComputePlaneRole, nil, nil)
	require.NoError(t, res.Err)

	crs := client.RbacV1().ClusterRoles()
	_, err := crs.Get(context.Background(), stale.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "another run's object past the TTL is reclaimed")
	_, err = crs.Get(context.Background(), live.Name, metav1.GetOptions{})
	assert.NoError(t, err, "an overlapping run's object is younger than the TTL on the apiserver's clock")
	_, err = crs.Get(context.Background(), clusterValidatorRBACName(clusterValidatorComputePlaneRole, res.RunID),
		metav1.GetOptions{})
	assert.NoError(t, err, "the run never sweeps its own objects")
	_, err = client.CoreV1().Secrets(clusterValidatorNamespace).Get(context.Background(), explicit.Name,
		metav1.GetOptions{})
	assert.NoError(t, err, "an explicitly named pull Secret is never swept")
	assert.Equal(t, explicit.Name, jobPullSecret(t, client))

	order := []string{}
	for _, a := range client.Actions() {
		switch {
		case a.GetVerb() == "delete" && a.GetResource().Resource == "clusterroles":
			order = append(order, "sweep")
		case a.GetVerb() == "create" && a.GetResource().Resource == "jobs":
			order = append(order, "job")
		}
	}
	assert.Equal(t, []string{"sweep", "job"}, order)
}

// jobPullSecret is the pull Secret the run's Job references.
func jobPullSecret(t *testing.T, client *fake.Clientset) string {
	t.Helper()
	for _, a := range client.Actions() {
		if c, ok := a.(ktesting.CreateAction); ok && a.GetResource().Resource == "jobs" {
			var names []string
			for _, s := range c.GetObject().(*batchv1.Job).Spec.Template.Spec.ImagePullSecrets {
				names = append(names, s.Name)
			}
			return strings.Join(names, ",")
		}
	}
	return ""
}

// A live run's objects must outlast it on the apiserver's clock: the longest
// a run takes, and the longest its pod can outlive the run, stay below the
// orphan TTL.
func TestClusterValidatorRunCeiling_StaysBelowTheOrphanTTL(t *testing.T) {
	prevStop, prevGrace := validatorStopTimeout, validatorDeadlineGrace
	validatorStopTimeout, validatorDeadlineGrace = defaultValidatorStopTimeout, defaultValidatorDeadlineGrace
	t.Cleanup(func() { validatorStopTimeout, validatorDeadlineGrace = prevStop, prevGrace })
	podLife := time.Duration(*buildClusterValidatorJob("j", "img:1", "", clusterValidatorComputePlaneRole, "r",
		clusterValidatorNoConfigName, false, nil).Spec.ActiveDeadlineSeconds)*time.Second +
		clusterValidatorTerminationGrace
	assert.Less(t, max(ClusterValidatorRunCeiling(), podLife), orphanValidatorRBACTTL)
	assert.Greater(t, validatorStopTimeout, clusterValidatorTerminationGrace,
		"a stop waits out the pod's termination grace period")
}
