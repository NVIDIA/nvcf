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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// -- probeStaleNamespaces --

func TestProbeStaleNamespaces_AbsentIsHealthy(t *testing.T) {
	// A namespace that doesn't exist is not stale — it has simply never been
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

func TestProbeStaleNamespaces_EmptyShellNoHelmSecrets(t *testing.T) {
	// Namespace exists and is Active but holds no Helm release secrets →
	// leftover empty shell from a partial helm uninstall.
	client := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf"},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	})
	stale, err := probeStaleNamespaces(context.Background(), client, []string{"nvcf"})
	require.NoError(t, err)
	require.Len(t, stale, 1)
	assert.Equal(t, "nvcf", stale[0].Name)
	assert.Contains(t, stale[0].Reason, "Helm release")
}

func TestProbeStaleNamespaces_HealthyReleaseNotStale(t *testing.T) {
	// Namespace exists and carries an owner=helm secret → active Helm release,
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

func TestProbeStaleNamespaces_MixedNamespaces(t *testing.T) {
	// One absent, one healthy, one terminating, one empty shell.
	now := metav1.Now()
	client := fake.NewSimpleClientset(
		// "sis" — healthy with a Helm release
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
		// "nvcf" — stuck terminating
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "nvcf", DeletionTimestamp: &now},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceTerminating},
		},
		// "api-keys" — empty shell
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "api-keys"},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		// "cassandra-system" — absent (not present in fake)
	)

	namespaces := []string{"cassandra-system", "sis", "nvcf", "api-keys"}
	stale, err := probeStaleNamespaces(context.Background(), client, namespaces)
	require.NoError(t, err)
	require.Len(t, stale, 2, "only nvcf (terminating) and api-keys (empty shell) should be stale")

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

func TestStaleNamespaceCheck_ProberErrorDegradestoWarning(t *testing.T) {
	// A prober that cannot contact the cluster must not fail the overall check
	// at error severity — it would produce false failures on transient network
	// issues or misconfigured kubeconfigs.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, fmt.Errorf("cluster unreachable")
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "warning", r.Severity,
		"prober errors must degrade to warning so transient failures do not block the operator")
	assert.Contains(t, r.Message, "cluster unreachable")
}

func TestStaleNamespaceCheck_StaleIsError(t *testing.T) {
	// A successfully detected stale namespace must fail at error severity so
	// anyFailed trips the non-zero exit code.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{{Name: "nvcf", Reason: "stuck Terminating"}}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf"}).Run(context.Background())
	assert.False(t, r.Passed)
	assert.Equal(t, "error", r.Severity,
		"detected stale namespaces must use error severity so the exit code is non-zero")
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "kubectl patch namespace nvcf")
}

func TestStaleNamespaceCheck_CleanPasses(t *testing.T) {
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return nil, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "sis"}).Run(context.Background())
	assert.True(t, r.Passed)
}

func TestStaleNamespaceCheck_MessageNamesAllStaleNamespaces(t *testing.T) {
	// The remediation command must name every stale namespace so the operator
	// can copy-paste it without having to cross-reference the check output.
	prober := func(_ context.Context, _ string, _ []string) ([]StaleNamespace, error) {
		return []StaleNamespace{
			{Name: "nvcf", Reason: "stuck Terminating"},
			{Name: "api-keys", Reason: "no Helm release"},
		}, nil
	}
	r := staleNamespaceCheck(prober, "", []string{"nvcf", "api-keys"}).Run(context.Background())
	assert.Contains(t, r.Message, "nvcf")
	assert.Contains(t, r.Message, "api-keys")
	assert.Contains(t, r.Message, "kubectl patch namespace nvcf")
	assert.Contains(t, r.Message, "kubectl delete namespace api-keys")
}
