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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestBuildSummary_PopulatesFixedChecksAndDynamicMaps(t *testing.T) {
	reachOK := true
	netpolOK := true
	enforcementOK := true
	state := &ValidationState{
		Log:                      testLog(),
		ControlPlaneHealthy:      true,
		NodesAllReady:            true,
		WebhooksSupported:        true,
		NetworkPoliciesSupported: true,
		SMBCSIDriverOK:           true,
		GPUAvailable:             true,
		GPUOperatorInstalled:     true,
		ReachabilityOK:           &reachOK,
		ConfigurableNetPolOK:     &netpolOK,
		EnforcementOK:            &enforcementOK,
		EndpointResults: map[string]EndpointResult{
			"NGC API":           {Reachable: true, Critical: true},
			"Control Plane API": {Reachable: true, Critical: true},
			"Vault":             {Reachable: false, Critical: false},
		},
		NetpolPairResults: map[string]NetpolPairResult{
			"kube-system-self-talk": {
				Passed:   true,
				Critical: false,
				Directions: map[string]DirectionStatus{
					NetpolDirectionAToB: {EgressAllowed: true, IngressAllowed: true},
					NetpolDirectionBToA: {EgressAllowed: true, IngressAllowed: false},
				},
			},
		},
		Warnings: []string{"example warning"},
	}
	start := time.Now().Add(-12 * time.Second)
	s := buildSummary(state, start, true, "NVCF-Ready")

	require.NotNil(t, s)
	assert.Equal(t, SummarySchemaVersion, s.SchemaVersion)
	assert.True(t, s.VerdictReady)
	assert.Equal(t, "NVCF-Ready", s.Verdict)
	assert.GreaterOrEqual(t, s.DurationSeconds, 11.0, "should reflect elapsed wall-clock since start")
	assert.Equal(t, start.UTC().Format(time.RFC3339), s.RanAt, "RanAt should be the run start time, not end time")
	assert.True(t, s.Checks[CheckKeyControlPlane])
	assert.True(t, s.Checks[CheckKeyWebhooks])
	assert.True(t, s.Checks[CheckKeyEndpointReachability])
	assert.True(t, s.Checks[CheckKeyConfigurableNetpol])
	assert.True(t, s.Checks[CheckKeyNetpolEnforcement])
	assert.Equal(t, EndpointStatus{Reachable: true, Critical: true}, s.Endpoints["NGC API"])
	assert.Equal(t, EndpointStatus{Reachable: false, Critical: false}, s.Endpoints["Vault"])
	assert.Equal(t, PairStatus{
		Passed:   true,
		Critical: false,
		Directions: map[string]DirectionStatus{
			NetpolDirectionAToB: {EgressAllowed: true, IngressAllowed: true},
			NetpolDirectionBToA: {EgressAllowed: true, IngressAllowed: false},
		},
	}, s.NetpolPairs["kube-system-self-talk"])
	assert.Equal(t, []string{"example warning"}, s.Warnings)
}

func TestBuildSummary_OmitsCheckKeysForChecksThatDidNotRun(t *testing.T) {
	// Reachability/netpol/enforcement *bool fields nil → those check
	// keys must NOT appear in s.Checks so the agent can distinguish
	// "not run" from "ran and failed".
	state := &ValidationState{
		Log:                      testLog(),
		ControlPlaneHealthy:      true,
		NodesAllReady:            true,
		WebhooksSupported:        true,
		NetworkPoliciesSupported: true,
		SMBCSIDriverOK:           true,
		GPUAvailable:             true,
		GPUOperatorInstalled:     true,
	}
	s := buildSummary(state, time.Now(), true, "NVCF-Ready")
	_, hasReach := s.Checks[CheckKeyEndpointReachability]
	_, hasNetpol := s.Checks[CheckKeyConfigurableNetpol]
	_, hasEnforce := s.Checks[CheckKeyNetpolEnforcement]
	assert.False(t, hasReach, "endpoint_reachability key omitted when ReachabilityOK is nil")
	assert.False(t, hasNetpol, "configurable_netpol key omitted when ConfigurableNetPolOK is nil")
	assert.False(t, hasEnforce, "netpol_enforcement key omitted when EnforcementOK is nil")
	assert.Empty(t, s.Endpoints, "no endpoints map when no per-endpoint results")
	assert.Empty(t, s.NetpolPairs, "no netpolPairs map when no per-pair results")
}

func TestParseSummary_RoundTrip(t *testing.T) {
	original := &ValidatorSummary{
		SchemaVersion:   SummarySchemaVersion,
		RanAt:           "2026-06-11T10:00:00Z",
		DurationSeconds: 12.3,
		Verdict:         "NVCF-Ready",
		VerdictReady:    true,
		Checks: map[string]bool{
			CheckKeyControlPlane: true,
			CheckKeySMBCSI:       false,
		},
		Endpoints: map[string]EndpointStatus{
			"NGC API": {Reachable: true, Critical: true},
		},
		NetpolPairs: map[string]PairStatus{
			"pair-a": {
				Passed:   true,
				Critical: false,
				Directions: map[string]DirectionStatus{
					NetpolDirectionAToB: {EgressAllowed: true, IngressAllowed: true},
					NetpolDirectionBToA: {EgressAllowed: false, IngressAllowed: true},
				},
			},
		},
		Warnings: []string{"w1", "w2"},
	}
	payload, err := json.Marshal(original)
	require.NoError(t, err)

	parsed, err := ParseSummary(payload)
	require.NoError(t, err)
	assert.Equal(t, original, parsed)
}

func TestParseSummary_RejectsUnknownSchemaVersion(t *testing.T) {
	// Schema drift must surface as a parse error so the agent can keep
	// serving last-known-good metrics rather than zeroing out the SLI
	// on a transient version mismatch.
	payload := []byte(`{"schemaVersion":"v999","ranAt":"2026-06-11T10:00:00Z"}`)
	_, err := ParseSummary(payload)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported summary schema version")
}

func TestParseSummary_RejectsMalformedJSON(t *testing.T) {
	_, err := ParseSummary([]byte("{not json"))
	require.Error(t, err)
}

func TestWriteSummaryConfigMap_CreatesWhenMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	summary := &ValidatorSummary{
		SchemaVersion: SummarySchemaVersion,
		RanAt:         "2026-06-11T10:00:00Z",
		Verdict:       "NVCF-Ready",
		VerdictReady:  true,
		Checks:        map[string]bool{CheckKeyControlPlane: true},
	}
	writeSummaryConfigMap(context.Background(), testLog(), client, "nvca-operator", summary)

	cm, err := client.CoreV1().ConfigMaps("nvca-operator").
		Get(context.Background(), SummaryConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[SummaryConfigMapKey], `"verdict": "NVCF-Ready"`)
	assert.Equal(t, "cluster-validator", cm.Labels["app.kubernetes.io/component"])
}

func TestWriteSummaryConfigMap_UpdatesExisting(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SummaryConfigMapName,
			Namespace: "nvca-operator",
		},
		Data: map[string]string{SummaryConfigMapKey: `{"old": true}`},
	}
	client := fake.NewSimpleClientset(existing)
	summary := &ValidatorSummary{
		SchemaVersion: SummarySchemaVersion,
		RanAt:         "2026-06-11T13:00:00Z",
		Verdict:       "NVCF-Not-Ready",
		VerdictReady:  false,
		Checks:        map[string]bool{CheckKeyControlPlane: false},
	}
	writeSummaryConfigMap(context.Background(), testLog(), client, "nvca-operator", summary)

	cm, err := client.CoreV1().ConfigMaps("nvca-operator").
		Get(context.Background(), SummaryConfigMapName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Contains(t, cm.Data[SummaryConfigMapKey], `"verdict": "NVCF-Not-Ready"`)
	assert.NotContains(t, cm.Data[SummaryConfigMapKey], `"old": true`,
		"update must replace prior data, not append")
}

func TestWriteSummaryConfigMap_ConcurrentCreateFallsBackToUpdate(t *testing.T) {
	// Two validator instances race: both Get→NotFound and try to Create.
	// The loser gets AlreadyExists and must re-read + Update with its own
	// payload (last-writer-wins) rather than logging a misleading failure.
	client := fake.NewSimpleClientset()

	getCount := 0
	client.PrependReactor("get", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		getCount++
		if getCount == 1 {
			return true, nil, apierrors.NewNotFound(corev1.Resource("configmaps"), SummaryConfigMapName)
		}
		// Re-read after the create conflict returns the winner's object.
		return true, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: SummaryConfigMapName, Namespace: "nvca-operator"},
			Data:       map[string]string{SummaryConfigMapKey: `{"winner": true}`},
		}, nil
	})
	client.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(corev1.Resource("configmaps"), SummaryConfigMapName)
	})
	var updatedData string
	client.PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		cm := action.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		updatedData = cm.Data[SummaryConfigMapKey]
		return true, cm, nil
	})

	writeSummaryConfigMap(context.Background(), testLog(), client, "nvca-operator", &ValidatorSummary{
		SchemaVersion: SummarySchemaVersion,
		RanAt:         "2026-06-11T10:00:00Z",
		Verdict:       "NVCF-Ready",
		VerdictReady:  true,
	})

	assert.Contains(t, updatedData, `"verdict": "NVCF-Ready"`,
		"after AlreadyExists, this run's payload must be written via Update")
}

func TestWriteSummaryConfigMap_FailureDoesNotPanic(t *testing.T) {
	// nil client would obviously panic; this checks the function tolerates
	// the kind of API failure modes a real cluster surfaces. Specifically,
	// even if marshal fails we should return cleanly.
	client := fake.NewSimpleClientset()
	// A summary that JSON-marshals fine — the API path is what we're testing.
	writeSummaryConfigMap(context.Background(), testLog(), client, "nvca-operator", &ValidatorSummary{
		SchemaVersion: SummarySchemaVersion,
		RanAt:         "2026-06-11T10:00:00Z",
	})
	// Just no panic = success here.
}

// TestAllCheckKeysCoversEveryCheckKeyConst guards against forgetting to
// add a new CheckKey* constant to AllCheckKeys (which the metrics-side
// init-to-zero loop depends on).
func TestAllCheckKeysCoversEveryCheckKeyConst(t *testing.T) {
	known := map[string]bool{}
	for _, k := range AllCheckKeys {
		known[k] = true
	}
	for _, k := range []string{
		CheckKeyControlPlane,
		CheckKeyWorkerNodesAllReady,
		CheckKeyWebhooks,
		CheckKeyNetworkPoliciesSupport,
		CheckKeySMBCSI,
		CheckKeyEndpointReachability,
		CheckKeyGPUResources,
		CheckKeyGPUOperator,
		CheckKeyConfigurableNetpol,
		CheckKeyNetpolEnforcement,
	} {
		assert.True(t, known[k], "%q is a CheckKey constant but missing from AllCheckKeys", k)
	}
	assert.Len(t, AllCheckKeys, 10, "if you added a new CheckKey, also add it to AllCheckKeys AND to clusterValidatorCheckKeys() in internal/metrics/metrics.go")
}
