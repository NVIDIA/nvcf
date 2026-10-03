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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// -- probeStaleNamespaces --

func TestProbeStaleNamespaces_AbsentIsHealthy(t *testing.T) {
	// A namespace that doesn't exist is not stale - it has simply never been
	// created or was already fully deleted.
	client := fake.NewSimpleClientset()
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf", "sis"})
	require.NoError(t, err)
	assert.Empty(t, stale, "absent namespaces must not be reported as stale")
}

func TestProbeStaleNamespaces_TerminatingIsByDeletionTimestamp(t *testing.T) {
	now := metav1.Now()
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &now},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Terminating")
}

func TestProbeStaleNamespaces_TerminatingIsByPhase(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "sis"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"sis"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "sis", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Terminating")
}

func TestProbeStaleNamespaces_NoReleaseWithInstallDataIsStale(t *testing.T) {
	// Namespace exists and is Active, holds no Helm release, and still holds a
	// volume claim -> leftover of a partial helm uninstall. The second
	// namespace carries a release and is not reported.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "nvcf"}},
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sis"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.sis.v1",
			Namespace: "sis",
			Labels:    map[string]string{"owner": "helm", "name": "sis", "status": "deployed"},
		}},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf", "sis"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Helm release")
}

func TestProbeStaleNamespaces_HealthyReleaseNotStale(t *testing.T) {
	// Namespace exists and carries an owner=helm secret -> active Helm release,
	// not stale.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sh.helm.release.v1.nvcf.v1",
				Namespace: "nvcf",
				Labels:    map[string]string{"owner": "helm", "name": "nvcf", "status": "deployed"},
			},
		},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, stale, "namespace with an active Helm release must not be stale")
}

func TestProbeStaleNamespaces_HealthyReleaseConfigMapDriverNotStale(t *testing.T) {
	// HELM_DRIVER=configmap stores release objects as ConfigMaps with owner=helm.
	// A namespace with an owner=helm ConfigMap and no Helm Secret must not be
	// reported as stale.
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "nvcf.v1",
				Namespace: "nvcf",
				Labels:    map[string]string{"owner": "helm", "name": "nvcf", "status": "deployed"},
			},
		},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, stale, "namespace with an owner=helm ConfigMap (configmap driver) must not be stale")
}

func TestProbeStaleNamespaces_MixedNamespaces(t *testing.T) {
	// One absent, one healthy, one terminating, one left with its data, and
	// one empty, as before a first install.
	now := metav1.Now()
	client := fake.NewSimpleClientset(
		// "sis" - healthy with a Helm release
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "sis"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "sh.helm.release.v1.sis.v1", Namespace: "sis",
				Labels: map[string]string{"owner": "helm"},
			},
		},
		// "nvcf" - stuck terminating
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &now},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
		// "api-keys" - no release, its volume claim left behind
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "api-keys"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "api-keys"}},
		// "ess" - empty, created ahead of a first install
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "ess"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		// "cassandra-system" - absent (not present in fake)
	)

	namespaces := []string{"cassandra-system", "sis", "nvcf", "api-keys", "ess"}
	stale, err := probeStaleNamespaces(context.Background(), client, namespaces)
	require.NoError(t, err)
	require.Len(t, stale, 2, "only nvcf (terminating) and api-keys (data left behind) should be stale")

	staleNames := make(map[string]string, 2)
	for _, s := range stale {
		staleNames[s.Name] = s.Reason
	}
	assert.Contains(t, staleNames, "nvcf")
	assert.Contains(t, staleNames["nvcf"], "Terminating")
	assert.Contains(t, staleNames, "api-keys")
	assert.Contains(t, staleNames["api-keys"], "Helm release")
}

// -- staleNamespaceCheck binaryCheckSpec --

// pinCurrentKubeContext keeps hint assertions independent of the developer's
// kubeconfig: without it, a machine with a current-context makes every hint
// carry a --context flag the test did not ask for.
func pinCurrentKubeContext(t *testing.T, name string) {
	t.Helper()
	prev := currentKubeContextNameFn
	currentKubeContextNameFn = func() string { return name }
	t.Cleanup(func() { currentKubeContextNameFn = prev })
}

func TestStaleNamespaceCheck_ProberErrorDegradestoWarning(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// A prober that cannot contact the cluster must not fail the overall check
	// at error severity - it would produce false failures on transient network
	// issues or misconfigured kubeconfigs.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, fmt.Errorf("cluster unreachable")
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityWarning, r.Severity,
		"prober errors must degrade to warning so transient failures do not block the operator")
	assert.Contains(t, r.Message, "cluster unreachable")
}

func TestStaleNamespaceCheck_StaleIsError(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// A successfully detected stale namespace must fail at error severity so
	// anyFailed trips the non-zero exit code.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityError, r.Severity,
		"detected stale namespaces must use error severity so the exit code is non-zero")
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "/api/v1/namespaces/nvcf/finalize",
		"the remediation must use the finalize subresource; a plain patch is silently reverted")
	inspect := strings.Index(r.Message, "xargs -n1 kubectl get -n nvcf")
	force := strings.Index(r.Message, "/finalize")
	require.GreaterOrEqual(t, inspect, 0)
	assert.Less(t, inspect, force, "inspecting the namespace comes before force-clearing its finalizers")
}

func TestStaleNamespaceCheck_CleanPasses(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())
	assert.True(t, r.Passed)
	assert.Equal(t, SeverityInfo, r.Severity, "a clean pass must not carry the error severity it starts with")
}

func TestStaleNamespaceCheck_MessageNamesAllStaleNamespaces(t *testing.T) {
	pinCurrentKubeContext(t, "")
	// The remediation command must name every stale namespace so the operator
	// can copy-paste it without having to cross-reference the check output.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "api-keys", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "api-keys"}).Run(context.Background())
	assert.Equal(t, SeverityError, r.Severity,
		"a namespace stuck Terminating fails the run whatever else is reported beside it")
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "api-keys")
	assert.Contains(t, r.Message, "/api/v1/namespaces/nvcf/finalize",
		"the remediation must use the finalize subresource; a plain patch is silently reverted")
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n api-keys --show-kind --ignore-not-found")
	assert.NotContains(t, r.Message, "get all", "get all omits PVCs, Secrets and custom resources")
}

// A namespace on a real install holds many non-Helm Secrets (ServiceAccount
// tokens, TLS, pull secrets) that sort before sh.helm.release.v1.*. The
// apiserver applies the label selector after paging, so the first page can
// legitimately come back empty with a Continue token. Treating that as "no
// release" reports a live namespace stale and tells the operator to delete it.
func TestProbeStaleNamespaces_PagesPastNonMatchingObjects(t *testing.T) {
	for _, resource := range []string{"secrets", "configmaps"} {
		t.Run(resource, func(t *testing.T) {
			client := fake.NewSimpleClientset(&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
				Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
			}, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "nvcf"}})

			// First page: empty with a Continue token. Second page: the
			// release object.
			calls := 0
			release := metav1.ObjectMeta{
				Name: "sh.helm.release.v1.nvcf.v1", Namespace: "nvcf",
				Labels: map[string]string{"owner": "helm", "name": "nvcf", "status": "deployed"},
			}
			client.PrependReactor("list", resource, func(a ktesting.Action) (bool, runtime.Object, error) {
				if !listSelects(a, "deployed") {
					return false, nil, nil
				}
				calls++
				if calls == 1 {
					if resource == "secrets" {
						return true, &corev1.SecretList{ListMeta: metav1.ListMeta{Continue: "next"}}, nil
					}
					return true, &corev1.ConfigMapList{ListMeta: metav1.ListMeta{Continue: "next"}}, nil
				}
				if resource == "secrets" {
					return true, &corev1.SecretList{Items: []corev1.Secret{{ObjectMeta: release}}}, nil
				}
				return true, &corev1.ConfigMapList{Items: []corev1.ConfigMap{{ObjectMeta: release}}}, nil
			})

			stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
			require.NoError(t, err)
			assert.Empty(t, stale,
				"an empty first page with a Continue token must not be read as 'no Helm release'")
			assert.Equal(t, 2, calls, "the probe must follow the Continue token")
		})
	}
}

// The remediation for a hit is "delete this namespace", so the list must only
// contain namespaces a helmfile release actually owns. nvcf-backend is created
// at runtime by NVCA and holds live worker pods.
func TestControlPlaneNamespaceList_ExcludesRuntimeOwnedNamespaces(t *testing.T) {
	cp := namespaceNames(nvcfControlPlaneNamespaces)
	for _, ns := range []string{"nvcf-backend", "openbao-system"} {
		assert.NotContains(t, cp, ns,
			"%s hosts no Helm release; probing it yields a destructive false positive", ns)
	}
	for _, ns := range []string{"cert-manager", "nvcf-ui"} {
		assert.Contains(t, cp, ns, "%s is a real stack namespace and must be probed", ns)
	}
	assert.NotContains(t, namespaceNames(nvcfComputePlaneNamespaces), "nvca-system",
		"nvca-system is operator-created and hosts no release")
}

func namespaceNames(list []stackNamespace) []string {
	out := make([]string, 0, len(list))
	for _, ns := range list {
		out = append(out, ns.name)
	}
	return out
}

// TestStaleNamespaceCheck_HintsPinTheProbedContext guards the remediation
// hints in split mode. The two callers probe different clusters, so a bare
// kubectl command in a hint runs against whatever context is current, and
// these hints delete namespaces.
func TestStaleNamespaceCheck_HintsPinTheProbedContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "api-keys", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "cp-ctx", []string{"nvcf", "api-keys"}).Run(context.Background())

	// Every kubectl invocation, including the one piped into xargs, has to
	// carry the flag: one unpinned command is enough to hit the wrong cluster.
	for _, cmd := range strings.Split(r.Message, "kubectl ")[1:] {
		assert.True(t, strings.HasPrefix(cmd, "--context cp-ctx "),
			"unpinned kubectl invocation in hint: kubectl %s", cmd)
	}
	assert.Contains(t, r.Message, "xargs -n1 kubectl --context cp-ctx get -n api-keys")
}

func TestStaleNamespaceCheck_HintsQuoteTheContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "my ctx", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "--context 'my ctx' get -n nvcf",
		"a context name with a space must be quoted so the pasted command does not split it")
}

// TestStaleNamespaceCheck_HintsSubstituteTheNamespace guards against a bare
// `<ns>` placeholder in a pasteable command: the shell reads `<` and `>` as
// redirection, so the command fails before kubectl runs.
func TestStaleNamespaceCheck_HintsSubstituteTheNamespace(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "sis", Reason: "stuck Terminating"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())

	assert.NotContains(t, r.Message, "<ns>",
		"a shell-redirection placeholder must not appear in a copy-pasteable command")
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n nvcf ")
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n sis ")
}

// TestStaleNamespaceCheck_HintsNameTheCurrentContext covers the single-cluster
// path. The probe followed the current-context, so the hint has to name it:
// the operator can switch context between reading this and pasting it, and
// these commands delete namespaces.
func TestStaleNamespaceCheck_HintsNameTheCurrentContext(t *testing.T) {
	pinCurrentKubeContext(t, "k3d-local")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "xargs -n1 kubectl --context k3d-local get -n nvcf")
}

// An unreadable kubeconfig must not break the hint; it degrades to no flag.
func TestStaleNamespaceCheck_HintsOmitUnknownContext(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n nvcf")
}

// The probe must run against the same context the hints name. Resolving the
// current-context after probing leaves a window where it changes in between,
// which would have the delete hints target a cluster that was never read.
func TestStaleNamespaceCheck_ProbesTheResolvedContext(t *testing.T) {
	pinCurrentKubeContext(t, "k3d-local")
	var probed string
	prober := func(_ context.Context, kctx string, _ []string) ([]StaleNamespace, error) {
		probed = kctx
		return nil, nil
	}
	staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Equal(t, "k3d-local", probed,
		"the probe must target the resolved context, not whatever is current when it runs")
}

// `down` destroys every release but keeps the namespaces and their PVCs, so no
// owner=helm object exists anywhere. With the default driver that is exactly
// the stale state to report; a reinstall would silently reattach the old
// Cassandra and OpenBao volumes. Helm never deletes hook Jobs, so their
// Completed pods are still there, and they are not a live install.
func TestProbeStaleNamespaces_AfterDownReportsEveryNamespace(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	var objects []runtime.Object
	for _, ns := range []string{"cassandra-system", "vault-system"} {
		objects = append(objects,
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-0", Namespace: ns}},
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "initialize-cluster", Namespace: ns}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "initialize-cluster-x1", Namespace: ns,
					OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: "initialize-cluster"}}},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
		)
	}
	client := fake.NewSimpleClientset(objects...)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"cassandra-system", "vault-system"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// Only a Pending or Running pod that is neither terminating nor a Job's is a
// live workload. A hook Job's pod, even one still running, and a pod being
// deleted do not keep a namespace holding install data from being reported.
func TestProbeStaleNamespaces_OnlyALiveWorkloadIsAnInstall(t *testing.T) {
	now := metav1.Now()
	pod := func(ns string, phase corev1.PodPhase, mutate func(*corev1.Pod)) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: ns}, Status: corev1.PodStatus{Phase: phase}}
		if mutate != nil {
			mutate(p)
		}
		return p
	}
	cases := map[string]struct {
		pod   *corev1.Pod
		stale bool
	}{
		"running":     {pod("a", corev1.PodRunning, nil), false},
		"pending":     {pod("b", corev1.PodPending, nil), false},
		"succeeded":   {pod("c", corev1.PodSucceeded, nil), true},
		"terminating": {pod("d", corev1.PodRunning, func(p *corev1.Pod) { p.DeletionTimestamp = &now }), true},
		"hook job's": {pod("e", corev1.PodRunning, func(p *corev1.Pod) {
			p.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: "migrations"}}
		}), true},
	}
	for name, tc := range cases {
		ns := tc.pod.Namespace
		client := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: ns}},
			tc.pod,
		)
		got, err := probeStaleNamespaces(context.Background(), client, []string{ns})
		require.NoError(t, err, name)
		assert.Equal(t, tc.stale, len(got) == 1, name)
	}
}

// HELM_DRIVER=sql keeps release state in a database, so the absence of an
// in-cluster release object says nothing and must not be reported.
func TestProbeStaleNamespaces_SQLDriverSkipsTheNoReleaseSignal(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
	)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

// A namespace stuck Terminating is still reported even when no Helm release
// object exists anywhere: that signal does not depend on the storage driver.
func TestProbeStaleNamespaces_TerminatingReportedWithoutHelmObjects(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	deleted := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewSimpleClientset(
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &deleted},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
	)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "stuck Terminating", got[0].Reason)
}

// A conditionally-disabled component installed the upstream way is healthy.
// Reporting it at error severity turns a supported shape into exit 2, and the
// old remediation would have destroyed every Certificate in the cluster.
func TestStaleNamespaceCheck_NoHelmReleaseWarnsAndDoesNotSuggestDelete(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "cert-manager", Reason: "no Helm release"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"cert-manager"}).Run(context.Background())

	assert.Equal(t, SeverityWarning, r.Severity,
		"a namespace with no Helm release must not fail the run")
	for _, nudge := range []string{" delete ", "remove", "To resolve"} {
		assert.NotContains(t, r.Message, nudge,
			"no Helm release is an observation; the hint must not frame removing the namespace as the fix")
	}
	assert.Contains(t, r.Message, "xargs -n1 kubectl get -n cert-manager --show-kind --ignore-not-found")
}

// A namespace stuck Terminating does block the run.
func TestStaleNamespaceCheck_TerminatingIsStillAnError(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.Equal(t, SeverityError, r.Severity)
	assert.False(t, r.Transient, "a stuck namespace does not clear by waiting")
}

// kubectl keeps only the last -n it is given, so a joined command inspects one
// namespace and reports the rest as empty, which is the opposite of what an
// operator deciding whether to delete them needs.
func TestStaleNamespaceCheck_HintsOneCommandPerNamespace(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "api-keys", Reason: "no Helm release"},
			{Name: "ess", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"api-keys", "ess"}).Run(context.Background())

	assert.NotContains(t, r.Message, "-n api-keys -n ess",
		"a joined namespace list silently inspects only the last one")
	assert.Contains(t, r.Message, "get -n api-keys --show-kind")
	assert.Contains(t, r.Message, "get -n ess --show-kind")
}

// The documented install pre-creates namespaces before the first install, so
// none has a release yet: labelled and empty, or holding a pull Secret, a TLS
// Secret, or the CA ConfigMap a mesh or the platform adds. None of that is a
// leftover. A namespace that still holds volume claims or workload objects, as
// `down` leaves them, is.
func TestProbeStaleNamespaces_PreCreatedNamespaceIsNotStale(t *testing.T) {
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"nvcf/platform": "true"}},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}}
	}
	secret := func(ns, name string, typ corev1.SecretType) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Type: typ}
	}
	configMap := func(ns, name string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	}
	client := fake.NewSimpleClientset(
		ns("api-keys"),
		ns("ess"), secret("ess", "nvcr-pull-secret", corev1.SecretTypeDockerConfigJson),
		ns("nvcf"), secret("nvcf", "stargate-quic-tls", corev1.SecretTypeTLS), configMap("nvcf", "kube-root-ca.crt"),
		ns("sis"), configMap("sis", "istio-ca-root-cert"), configMap("sis", "openshift-service-ca.crt"),
		ns("cassandra-system"), secret("cassandra-system", "nvcr-pull-secret", corev1.SecretTypeDockerConfigJson),
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-cassandra-0", Namespace: "cassandra-system"}},
		ns("vault-system"), secret("vault-system", "vault-unseal", corev1.SecretTypeOpaque),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "openbao-server", Namespace: "vault-system"}},
	)
	stale, err := probeStaleNamespaces(context.Background(), client,
		[]string{"api-keys", "ess", "nvcf", "sis", "cassandra-system", "vault-system"})
	require.NoError(t, err)
	var names []string
	for _, s := range stale {
		names = append(names, s.Name)
	}
	assert.ElementsMatch(t, []string{"cassandra-system", "vault-system"}, names,
		"only the namespaces that still hold volume claims or workload objects")
}

// An install rendered with `helm template` (Argo CD) records no Helm release
// but runs pods. That is a live install, not a leftover, though it holds what
// a leftover would.
func TestProbeStaleNamespaces_NamespaceRunningPodsIsNotStale(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nats-system"},
			Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "nats", Namespace: "nats-system"}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "nats-js-nats-0", Namespace: "nats-system"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "nats-0", Namespace: "nats-system"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "nats-auth", Namespace: "nats-system"},
			Type: corev1.SecretTypeOpaque},
	)
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nats-system"})
	require.NoError(t, err)
	assert.Empty(t, stale)
}

// listSelects reports whether a list action's label selector names value.
func listSelects(a ktesting.Action, value string) bool {
	return strings.Contains(a.(ktesting.ListAction).GetListRestrictions().Labels.String(), value)
}

func forbidden(resource, name string) error {
	return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, name, errors.New("denied"))
}

// One namespace that cannot be read must not hide what the others show: a
// namespace stuck Terminating is still reported, before or after the read
// that failed, and the failures name their namespaces.
func TestProbeStaleNamespaces_KeepsProbingPastAReadError(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	deleted := metav1.NewTime(time.Now().Add(-10 * time.Minute))
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cassandra-system", DeletionTimestamp: &deleted}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vault-system"}},
	)
	client.PrependReactor("get", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.(ktesting.GetAction).GetName() == "nvcf" {
			return true, nil, forbidden("namespaces", "nvcf")
		}
		return false, nil, nil
	})
	client.PrependReactor("list", "secrets", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("secrets", "")
	})

	stale, err := probeStaleNamespaces(context.Background(), client,
		[]string{"nvcf", "cassandra-system", "vault-system"})
	require.Len(t, stale, 1, "the namespace read after the failed one must still be probed")
	assert.Equal(t, "cassandra-system", stale[0].Name)
	assert.Equal(t, StaleStuckTerminating, stale[0].Reason)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get namespace nvcf")
	assert.Contains(t, err.Error(), "; vault-system: list Helm secrets")
	assert.NotContains(t, err.Error(), "\n", "the errors are rendered on the row's one line")
	assert.True(t, apierrors.IsForbidden(err), "the underlying API errors stay reachable")
}

// A stuck namespace found next to a probe error keeps the row at error, and
// the row names both. Grading the pair as the probe error alone let `check
// --pre` pass over a namespace `up` would then fail to create into.
func TestStaleNamespaceCheck_StuckStaysAnErrorBesideAProbeError(t *testing.T) {
	pinCurrentKubeContext(t, "")
	readErr := errors.New("vault-system: list Helm secrets: forbidden")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "cassandra-system", Reason: StaleStuckTerminating}}, readErr
	}
	r := staleNamespaceCheck(prober, "", nil).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, SeverityError, r.Severity)
	assert.Contains(t, r.Message, "cassandra-system (stuck Terminating)")
	assert.Contains(t, r.Message, "could not probe: vault-system")
	assert.ErrorIs(t, r.Err, readErr)

	// Without a stuck namespace the same pair is a warning that still lists
	// what was found.
	prober = func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "api-keys", Reason: StaleNoHelmRelease}}, readErr
	}
	r = staleNamespaceCheck(prober, "", nil).Run(context.Background())
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.Contains(t, r.Message, "api-keys (no Helm release)")
	assert.Contains(t, r.Message, "could not probe: vault-system")
}

// A namespace a moment into a normal deletion is draining volumes and
// finalizers; `up` waits for it. Only past the bound, or once the namespace
// controller reports a deletion failure, is it stuck.
func TestProbeStaleNamespaces_StuckOnlyPastTheBoundOrOnAFailure(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	ago := func(d time.Duration) *metav1.Time { ts := metav1.NewTime(time.Now().Add(-d)); return &ts }
	cond := func(t corev1.NamespaceConditionType) []corev1.NamespaceCondition {
		return []corev1.NamespaceCondition{{Type: t, Status: corev1.ConditionTrue, Message: "held"}}
	}
	soon, late := ago(5*time.Second), ago(namespaceStuckAfter+time.Second)
	cases := map[string]struct {
		deleted    *metav1.Time
		conditions []corev1.NamespaceCondition
		want       string
	}{
		"just deleted":            {soon, nil, StaleTerminating},
		"finalizers draining":     {soon, cond(corev1.NamespaceFinalizersRemaining), StaleTerminating},
		"past the bound":          {late, nil, StaleStuckTerminating},
		"content failure":         {soon, cond(corev1.NamespaceDeletionContentFailure), StaleStuckTerminating},
		"discovery failure":       {soon, cond(corev1.NamespaceDeletionDiscoveryFailure), StaleStuckTerminating},
		"group-version failure":   {soon, cond(corev1.NamespaceDeletionGVParsingFailure), StaleStuckTerminating},
		"phase only, no failure":  {nil, nil, StaleTerminating},
		"phase only, and failing": {nil, cond(corev1.NamespaceDeletionContentFailure), StaleStuckTerminating},
	}
	for name, tc := range cases {
		client := fake.NewSimpleClientset(&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: tc.deleted},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating, Conditions: tc.conditions},
		})
		got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
		require.NoError(t, err, name)
		require.Len(t, got, 1, name)
		assert.Equal(t, tc.want, got[0].Reason, name)
	}
}

// A namespace still being deleted is a warning with a read-only hint: no
// /finalize, which would orphan the volumes it is still detaching.
func TestStaleNamespaceCheck_TerminatingWarnsWithoutTheFinalizeHint(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: StaleTerminating, Detail: "deleting for 5s"}}, nil
	}
	r := staleNamespaceCheck(prober, "", nil).Run(context.Background())
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.True(t, r.Transient, "a deletion in progress clears by itself, so --wait polls it")
	assert.Contains(t, r.Message, "nvcf (Terminating: deleting for 5s)")
	assert.Contains(t, r.Message, "kubectl get ns nvcf -o jsonpath='{.status.conditions}'")
	assert.NotContains(t, r.Message, "finalize")
}

// The stuck hint reads the namespace's conditions before anything else.
func TestStaleNamespaceCheck_StuckHintReadsConditionsFirst(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: StaleStuckTerminating}}, nil
	}
	r := staleNamespaceCheck(prober, "", nil).Run(context.Background())
	conditions := strings.Index(r.Message, "get ns nvcf -o jsonpath='{.status.conditions}'")
	inspect := strings.Index(r.Message, "xargs -n1 kubectl get -n nvcf")
	require.GreaterOrEqual(t, conditions, 0)
	assert.Less(t, conditions, inspect)
}

func helmRecord(ns, name, status string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "sh.helm.release.v1." + name + ".v1", Namespace: ns,
		Labels: map[string]string{"owner": "helm", "name": name, "status": status},
	}}
}

// Only a deployed, failed or superseded record is a release `helm upgrade
// --install` builds on. One an interrupted install, upgrade or teardown left
// behind makes the next install fail, so it is reported by name and status;
// an uninstalled record kept by --keep-history is no release at all.
func TestProbeStaleNamespaces_GradesReleasesByStatus(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	const mid = StaleReleaseMidOperation
	cases := map[string]struct {
		statuses []string
		reason   string
		detail   string
	}{
		"deployed":           {[]string{"deployed"}, "", ""},
		"failed":             {[]string{"failed"}, "", ""},
		"superseded":         {[]string{"superseded"}, "", ""},
		"uninstalling":       {[]string{"uninstalling"}, mid, "cassandra uninstalling"},
		"pending-install":    {[]string{"pending-install"}, mid, "cassandra pending-install"},
		"pending-upgrade":    {[]string{"deployed", "pending-upgrade"}, mid, "cassandra pending-upgrade"},
		"pending-rollback":   {[]string{"pending-rollback"}, mid, "cassandra pending-rollback"},
		"uninstalled (kept)": {[]string{"uninstalled"}, StaleNoHelmRelease, ""},
	}
	for name, tc := range cases {
		objects := []runtime.Object{
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cassandra-system"}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "cassandra-system"}},
		}
		for i, status := range tc.statuses {
			rec := helmRecord("cassandra-system", "cassandra", status)
			rec.Name = fmt.Sprintf("%s%d", rec.Name, i)
			objects = append(objects, rec)
		}
		got, err := probeStaleNamespaces(context.Background(), fake.NewSimpleClientset(objects...),
			[]string{"cassandra-system"})
		require.NoError(t, err, name)
		if tc.reason == "" {
			assert.Empty(t, got, name)
			continue
		}
		require.Len(t, got, 1, name)
		assert.Equal(t, tc.reason, got[0].Reason, name)
		assert.Equal(t, tc.detail, got[0].Detail, name)
	}
}

// The ConfigMap driver's records are graded the same way.
func TestProbeStaleNamespaces_ConfigMapReleaseMidOperation(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nats-system"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "nats.v2", Namespace: "nats-system",
			Labels: map[string]string{"owner": "helm", "name": "nats", "status": "pending-upgrade"}}},
	)
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nats-system"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"nats"}, got[0].Releases)
}

// A release left mid-operation is a warning, since an install may still be
// running, and its hint reads the history on the probed context first.
func TestStaleNamespaceCheck_ReleaseMidOperationHint(t *testing.T) {
	pinCurrentKubeContext(t, "")
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "cassandra-system", Reason: StaleReleaseMidOperation,
			Detail: "cassandra uninstalling", Releases: []string{"cassandra"}}}, nil
	}
	r := staleNamespaceCheck(prober, "cp ctx", nil).Run(context.Background())
	assert.Equal(t, SeverityWarning, r.Severity)
	assert.Contains(t, r.Message, "cassandra-system (Helm release mid-operation: cassandra uninstalling)")
	assert.Contains(t, r.Message, "helm --kube-context 'cp ctx' history cassandra -n cassandra-system")
}

// A namespace whose pods or leftovers cannot be listed is listed as unread,
// not reported as a leftover nobody saw, and not passed over in silence.
func TestProbeStaleNamespaces_UnreadableWorkloadIsListedNotReported(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	for _, resource := range []string{"pods", "persistentvolumeclaims"} {
		client := fake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "api-keys"}},
			&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "api-keys"}},
		)
		client.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, forbidden(resource, "")
		})
		got, err := probeStaleNamespaces(context.Background(), client, []string{"api-keys"})
		assert.Empty(t, got, resource)
		require.Error(t, err, resource)
		assert.Contains(t, err.Error(), "api-keys: list "+resource, resource)
	}
}

// Each leftover kind is enough on its own, and one kind seen outweighs another
// that could not be read.
func TestHoldsInstallLeftovers_EachKindAndTheErrorDirection(t *testing.T) {
	ns := "vault-system"
	meta := metav1.ObjectMeta{Name: "x", Namespace: ns}
	kinds := map[string]runtime.Object{
		"persistentvolumeclaims": &corev1.PersistentVolumeClaim{ObjectMeta: meta},
		"jobs":                   &batchv1.Job{ObjectMeta: meta},
		"statefulsets":           &appsv1.StatefulSet{ObjectMeta: meta},
		"deployments":            &appsv1.Deployment{ObjectMeta: meta},
		"daemonsets":             &appsv1.DaemonSet{ObjectMeta: meta},
	}
	for kind, obj := range kinds {
		got, err := holdsInstallLeftovers(context.Background(), fake.NewSimpleClientset(obj), ns)
		require.NoError(t, err, kind)
		assert.True(t, got, kind)

		// Every other kind unreadable: what was seen still counts.
		client := fake.NewSimpleClientset(obj)
		for other := range kinds {
			if other != kind {
				client.PrependReactor("list", other, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, forbidden(other, "")
				})
			}
		}
		got, err = holdsInstallLeftovers(context.Background(), client, ns)
		require.NoError(t, err, kind)
		assert.True(t, got, kind)
	}

	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "jobs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("jobs", "")
	})
	got, err := holdsInstallLeftovers(context.Background(), client, ns)
	assert.False(t, got, "a kind that could not be read is not a leftover that was seen")
	assert.Error(t, err)

	got, err = holdsInstallLeftovers(context.Background(), fake.NewSimpleClientset(), ns)
	assert.False(t, got)
	assert.NoError(t, err)
}

func TestHasLiveWorkload_ReadErrorIsNotAWorkload(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden("pods", "")
	})
	live, err := hasLiveWorkload(context.Background(), client, "nvcf")
	assert.False(t, live)
	assert.Error(t, err)
}

// The budget running out mid-scan has to reach the result as the context's
// error, so the runner grades the row as cut short (exit 5) rather than as a
// clean pass or a warning.
func TestProbeStaleNamespaces_BudgetCutOffIsReturned(t *testing.T) {
	t.Setenv("HELM_DRIVER", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sis"}},
	)
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, nil, context.Canceled
	})
	_, err := probeStaleNamespaces(ctx, client, []string{"nvcf", "sis"})
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "sis", "the scan stops once the context is done")

	// A budget that runs out between two namespaces fails no call, and the
	// namespaces never read must still turn the scan into an error.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	client = fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "sis"}},
	)
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		cancel()
		return true, &corev1.PodList{}, nil
	})
	_, err = probeStaleNamespaces(ctx, client, []string{"nvcf", "sis"})
	require.ErrorIs(t, err, context.Canceled, "an unfinished scan is never a clean one")

	deadline := fmt.Errorf("list pods: %w", context.DeadlineExceeded)
	prober := func(context.Context, string, []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "api-keys", Reason: StaleNoHelmRelease}}, deadline
	}
	pinCurrentKubeContext(t, "")
	r := staleNamespaceCheck(prober, "", nil).Run(context.Background())
	assert.ErrorIs(t, r.Err, context.DeadlineExceeded,
		"findings beside a spent budget must still carry the budget error to the runner")
}

func TestFindHelmReleases_PageCapIsAnError(t *testing.T) {
	calls := 0
	list := func(metav1.ListOptions) ([]map[string]string, string, error) {
		calls++
		return nil, "more", nil
	}
	got, err := findHelmReleases(list, liveReleaseSelector, true)
	require.Error(t, err, "running out of pages is not proof that no release exists")
	assert.Nil(t, got)
	assert.Equal(t, helmReleaseListMaxPages, calls)
}

func TestHelmReleasesAreInCluster_NormalisesTheDriver(t *testing.T) {
	for driver, want := range map[string]bool{
		"": true, "secret": true, "configmap": true, "sql": false, " SQL ": false, "Sql\n": false,
	} {
		t.Setenv("HELM_DRIVER", driver)
		assert.Equal(t, want, helmReleasesAreInCluster(), "%q", driver)
	}
}

// Under HELM_DRIVER=sql the release lists say nothing, so they are not read:
// an error from them cannot cut the probe short.
func TestProbeStaleNamespaces_SQLDriverSkipsTheReleaseLists(t *testing.T) {
	t.Setenv("HELM_DRIVER", "sql")
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nvcf"}})
	for _, resource := range []string{"secrets", "configmaps", "pods"} {
		client.PrependReactor("list", resource, func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, forbidden(resource, "")
		})
	}
	got, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	assert.Empty(t, got)
}
