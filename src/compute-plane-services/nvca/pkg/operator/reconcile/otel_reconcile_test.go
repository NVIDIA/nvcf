/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package operator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
)

const (
	otelCollectorTokenURLProd    = "https://fnds-oauth.example.test/token"
	otelCollectorTokenURLStage   = "https://stage-fnds-oauth.example.test/token"
	otelCollectorGenericTokenURL = "https://generic-oauth.example.test/token"
)

func TestGetFNDSEndpoint(t *testing.T) {
	tests := []struct {
		name           string
		fndsCfg        *nvidiaiov1.FNDServiceConfig
		envType        nvidiaiov1.EnvType
		expectedResult string
	}{
		{
			name:           "Nil config - prod env - returns prod default",
			fndsCfg:        nil,
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLProd,
		},
		{
			name:           "Nil config - stage env - returns stage default",
			fndsCfg:        nil,
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLStg,
		},
		{
			name:           "Empty ServiceURL - prod env - returns prod default",
			fndsCfg:        &nvidiaiov1.FNDServiceConfig{},
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLProd,
		},
		{
			name:           "Empty ServiceURL - stage env - returns stage default",
			fndsCfg:        &nvidiaiov1.FNDServiceConfig{},
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: nvidiaiov1.FunctionDeploymentStagesServiceURLStg,
		},
		{
			name: "Custom ServiceURL - returns custom URL regardless of envType",
			fndsCfg: &nvidiaiov1.FNDServiceConfig{
				ServiceURL: "https://custom-fnds.example.com",
			},
			envType:        nvidiaiov1.EnvTypeProd,
			expectedResult: "https://custom-fnds.example.com",
		},
		{
			name: "Custom ServiceURL with stage env - returns custom URL",
			fndsCfg: &nvidiaiov1.FNDServiceConfig{
				ServiceURL: "https://custom-fnds.example.com",
			},
			envType:        nvidiaiov1.EnvTypeStage,
			expectedResult: "https://custom-fnds.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getFNDSEndpoint(tt.fndsCfg, tt.envType)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestGetOTelCollectorConfigData(t *testing.T) {
	tests := []struct {
		name                   string
		nb                     *nvidiaiov1.NVCFBackend
		expectedAuthenticator  string
		unexpectedExtension    string
		expectedPlaceholders   []string
		unexpectedPlaceholders []string
	}{
		{
			name:                  "service API key authentication",
			nb:                    &nvidiaiov1.NVCFBackend{},
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
			unexpectedExtension:   NVCAOTelCollectorAuthenticatorOAuth2Client,
			expectedPlaceholders:  []string{"${env:NGC_SERVICE_API_KEY_FILE}"},
			unexpectedPlaceholders: []string{
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_ID}",
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_SECRET_FILE}",
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_TOKEN_URL}",
			},
		},
		{
			name: "OAuth authentication",
			nb: &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{
				NVCFBackendSpecT: otelAuthSpec(true, "client-id", "", "", "", ""),
			}},
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
			unexpectedExtension:   NVCAOTelCollectorAuthenticatorBearerTokenAuth,
			expectedPlaceholders: []string{
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_ID}",
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_CLIENT_SECRET_FILE}",
				"${env:NVCA_OTEL_COLLECTOR_OAUTH_TOKEN_URL}",
			},
			unexpectedPlaceholders: []string{"${env:NGC_SERVICE_API_KEY_FILE}"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{}
			configData, err := bc.getOTelCollectorConfigData(tt.nb)
			require.NoError(t, err)
			require.Contains(t, configData, "config.yaml")
			config := configData["config.yaml"]

			var parsed struct {
				Extensions map[string]any `json:"extensions"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(config), &parsed))
			assert.Contains(t, parsed.Extensions, tt.expectedAuthenticator)
			assert.NotContains(t, parsed.Extensions, tt.unexpectedExtension)
			assert.Contains(t, parsed.Extensions, "health_check")

			for _, placeholder := range tt.expectedPlaceholders {
				assert.Contains(t, config, placeholder)
			}
			for _, placeholder := range tt.unexpectedPlaceholders {
				assert.NotContains(t, config, placeholder)
			}

			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_MEMORY_LIMIT_PERCENTAGE}")
			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_SPIKE_LIMIT_PERCENTAGE}")
			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_HEALTH_CHECK_PORT}")
			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_FNDS_ENDPOINT}")
			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_METRICS_PORT}")
			assert.Contains(t, config, "${env:NVCA_OTEL_COLLECTOR_AUTHENTICATOR}")
			assert.Contains(t, config, "memory_limiter")
			assert.Contains(t, config, "otlphttp")
			// Assert receiver/pipeline and Pod-lane semantics structurally.
			assertClusterWideK8sObjects(t, config)
			assertPodLaneTransforms(t, config)
			assertICMSLane(t, config)
		})
	}
}

// assertClusterWideK8sObjects parses the rendered collector config and asserts
// the namespace-scoped k8s_events receiver was replaced by a cluster-wide
// k8sobjects watch (with initial-state re-list) wired into the logs pipeline.
func assertClusterWideK8sObjects(t *testing.T, config string) {
	t.Helper()
	var full map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(config), &full))

	receivers, ok := full["receivers"].(map[string]any)
	require.True(t, ok, "receivers must be a map")
	assert.NotContains(t, receivers, "k8s_events", "namespace-scoped k8s_events must be removed")
	recv, ok := receivers["k8sobjects"].(map[string]any)
	require.True(t, ok, "k8sobjects receiver must be configured")
	assert.Equal(t, true, recv["include_initial_state"], "collector must re-list Events on start")
	objs, ok := recv["objects"].([]any)
	require.True(t, ok, "k8sobjects.objects must be a list")
	require.Len(t, objs, 1)
	obj0, ok := objs[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "events", obj0["name"])
	assert.Equal(t, "watch", obj0["mode"])

	logs := full["service"].(map[string]any)["pipelines"].(map[string]any)["logs"].(map[string]any)
	assert.Contains(t, logs["receivers"], "k8sobjects", "logs pipeline must read from k8sobjects")
}

// assertPodLaneTransforms asserts the Pod-lane #813 semantics: the task-id Pod
// label is extracted as task_id, the task_id namespace override runs after the
// function_version_id default, and both Pod event_name branches are present.
func assertPodLaneTransforms(t *testing.T, config string) {
	t.Helper()
	var full map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(config), &full))
	processors := full["processors"].(map[string]any)

	k8sattr := processors["k8sattributes"].(map[string]any)
	labels := k8sattr["extract"].(map[string]any)["labels"].([]any)
	tagByKey := map[string]string{}
	for _, l := range labels {
		m := l.(map[string]any)
		tagByKey[m["key"].(string)] = m["tag_name"].(string)
	}
	assert.Equal(t, "task_id", tagByKey["task-id"], "task-id label must map to task_id")

	var stmts []string
	for _, s := range processors["transform"].(map[string]any)["log_statements"].([]any) {
		stmts = append(stmts, s.(string))
	}
	fvIdx := indexOfContaining(stmts, `"namespace"], resource.attributes["function_version_id"]`)
	taskIdx := indexOfContaining(stmts, `"namespace"], resource.attributes["task_id"]`)
	require.GreaterOrEqual(t, fvIdx, 0, "function_version_id namespace statement missing")
	require.GreaterOrEqual(t, taskIdx, 0, "task_id namespace override missing")
	assert.Less(t, fvIdx, taskIdx, "task_id override must run after the function_version_id default")
	assert.GreaterOrEqual(t, indexOfContaining(stmts,
		`Concat(["Pod", resource.attributes["k8s.container.name"], log.attributes["k8s.event.reason"]]`), 0,
		"container-scoped event_name branch missing")
	assert.GreaterOrEqual(t, indexOfContaining(stmts,
		`Concat(["Pod", log.attributes["k8s.event.reason"]]`), 0,
		"pod-scoped event_name branch missing")
}

// assertICMSLane asserts the #814 ICMS lane semantics: a logs/icms-events pipeline
// is declared alongside the Pod lane, filter/icms-events keeps only ICMSRequest Events,
// transform/lift-icms-annotations lifts all nvcf.nvidia.io/* annotations, and
// transform/synth-icms-event-name builds both event_name variants.
func assertICMSLane(t *testing.T, config string) {
	t.Helper()
	var full map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(config), &full))
	processors := full["processors"].(map[string]any)

	icmsFilter, ok := processors["filter/icms-events"].(map[string]any)
	require.True(t, ok, "filter/icms-events processor must be declared")
	records := icmsFilter["logs"].(map[string]any)["log_record"].([]any)
	require.Len(t, records, 2, "filter/icms-events must have kind and reason drop conditions")
	// First condition must drop non-ICMSRequest kinds (exclusion, not inclusion).
	assert.Contains(t, records[0].(string), `!= "ICMSRequest"`, "filter/icms-events kind condition must drop non-ICMSRequest events")
	// Second condition must allowlist the four supported reasons via not(...in[...]).
	assert.Contains(t, records[1].(string), "not(", "filter/icms-events reason condition must use not() to drop unsupported reasons")
	for _, reason := range []string{"InstanceCreation", "InstanceStatusUpdate", "InstanceTermination", "ModelCaching"} {
		assert.Contains(t, records[1].(string), reason, "filter/icms-events allowlist must include reason %q", reason)
	}

	liftProc, ok := processors["transform/lift-icms-annotations"].(map[string]any)
	require.True(t, ok, "transform/lift-icms-annotations processor must be declared")
	var liftStmts []string
	for _, s := range liftProc["log_statements"].([]any) {
		liftStmts = append(liftStmts, s.(string))
	}
	for _, key := range []string{
		"nvcf.nvidia.io/icms-request-id",
		"nvcf.nvidia.io/function-version-id",
		"nvcf.nvidia.io/task-id",
		"nvcf.nvidia.io/instance-id",
		"nvcf.nvidia.io/cluster-id",
		"nvcf.nvidia.io/nca-id",
		"nvcf.nvidia.io/function-id",
		"nvcf.nvidia.io/instance-state",
		"nvcf.nvidia.io/failure-category",
		"nvcf.nvidia.io/termination-cause",
		"nvcf.nvidia.io/region",
		"nvcf.nvidia.io/status",
	} {
		assert.GreaterOrEqual(t, indexOfContaining(liftStmts, key), 0,
			"lift-icms-annotations must reference annotation %q", key)
	}
	fvIdx := indexOfContaining(liftStmts, `"nvcf.nvidia.io/function-version-id"`)
	taskIdx := indexOfContaining(liftStmts, `"nvcf.nvidia.io/task-id"`)
	require.GreaterOrEqual(t, fvIdx, 0, "function-version-id lift statement missing")
	require.GreaterOrEqual(t, taskIdx, 0, "task-id lift statement missing")
	assert.Less(t, fvIdx, taskIdx, "task-id must override namespace after function-version-id")
	// Assert complete source-to-destination mapping for the two non-obvious attributes.
	regionIdx := indexOfContaining(liftStmts, `"nvcf.nvidia.io/region"`)
	if assert.GreaterOrEqual(t, regionIdx, 0, "region lift statement missing") {
		assert.Contains(t, liftStmts[regionIdx], `set(log.attributes["region"]`)
	}
	statusIdx := indexOfContaining(liftStmts, `"nvcf.nvidia.io/status"`)
	if assert.GreaterOrEqual(t, statusIdx, 0, "status lift statement missing") {
		assert.Contains(t, liftStmts[statusIdx], `set(log.attributes["icms_status"]`)
	}

	synthProc, ok := processors["transform/synth-icms-event-name"].(map[string]any)
	require.True(t, ok, "transform/synth-icms-event-name processor must be declared")
	var synthStmts []string
	for _, s := range synthProc["log_statements"].([]any) {
		synthStmts = append(synthStmts, s.(string))
	}
	stateIdx := indexOfContaining(synthStmts,
		`Concat(["ICMSRequest", log.attributes["k8s.event.reason"], log.attributes["instance_state"]]`)
	// Use require so a missing branch stops execution before the indexed access below.
	require.GreaterOrEqual(t, stateIdx, 0, "state-qualified event_name branch missing")
	// Empty instance_state must fall through to the reason-only branch, not produce a trailing dot.
	assert.Contains(t, synthStmts[stateIdx], `instance_state"] != ""`,
		"state-qualified branch must guard against empty instance_state")
	reasonOnlyIdx := indexOfContaining(synthStmts,
		`Concat(["ICMSRequest", log.attributes["k8s.event.reason"]]`)
	// Use require so a missing branch stops execution before the indexed access below.
	require.GreaterOrEqual(t, reasonOnlyIdx, 0, "reason-only event_name branch missing")
	// Empty instance_state (not just nil) must also trigger the reason-only branch so
	// events with an empty annotation do not lose their event_name and get dropped by
	// filter/required-fields.
	assert.Contains(t, synthStmts[reasonOnlyIdx], `instance_state"] == ""`,
		"reason-only branch must accept empty instance_state, not only nil")

	pipelines := full["service"].(map[string]any)["pipelines"].(map[string]any)
	icmsPipeline, ok := pipelines["logs/icms-events"].(map[string]any)
	require.True(t, ok, "logs/icms-events pipeline must be declared")
	assert.Contains(t, icmsPipeline["receivers"], "k8sobjects")
	var pipelineProcs []string
	for _, p := range icmsPipeline["processors"].([]any) {
		pipelineProcs = append(pipelineProcs, p.(string))
	}
	for _, required := range []string{
		"memory_limiter", "transform/normalize", "filter/icms-events",
		"transform/lift-icms-annotations", "transform/synth-icms-event-name",
		"filter/required-fields", "batch",
	} {
		assert.Contains(t, pipelineProcs, required, "logs/icms-events pipeline missing processor %q", required)
	}
	assert.Contains(t, icmsPipeline["exporters"], "otlphttp/fnds")
	// Verify critical processor ordering.
	normalizeIdx := indexOfContaining(pipelineProcs, "transform/normalize")
	filterIdx := indexOfContaining(pipelineProcs, "filter/icms-events")
	liftIdx := indexOfContaining(pipelineProcs, "transform/lift-icms-annotations")
	synthProcIdx := indexOfContaining(pipelineProcs, "transform/synth-icms-event-name")
	requiredFieldsIdx := indexOfContaining(pipelineProcs, "filter/required-fields")
	assert.Less(t, normalizeIdx, filterIdx, "transform/normalize must run before filter/icms-events")
	assert.Less(t, liftIdx, synthProcIdx, "transform/lift-icms-annotations must run before transform/synth-icms-event-name")
	assert.Less(t, synthProcIdx, requiredFieldsIdx, "transform/synth-icms-event-name must run before filter/required-fields")
}

func indexOfContaining(items []string, sub string) int {
	for i, s := range items {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}

// otelAuthSpec builds minimal NVCFBackendSpecT for getOTelCollectorAuthConfig tests.
// empty value for vaultPath means use default; empty version means pre-2.51 (oauth-client-secrets.env).
func otelAuthSpec(vaultEnabled bool, clientID, prodTokenURL, stageTokenURL, version, vaultPath string) nvidiaiov1.NVCFBackendSpecT {
	spec := nvidiaiov1.NVCFBackendSpecT{
		VaultConfig: nvidiaiov1.VaultConfig{Enabled: vaultEnabled},
		OAuthConfig: nvidiaiov1.OAuthConfig{ClientID: clientID, TokenURL: otelCollectorGenericTokenURL},
		AgentConfig: nvidiaiov1.AgentConfig{
			FunctionDeploymentStagesProdOAuthTokenURL:  prodTokenURL,
			FunctionDeploymentStagesStageOAuthTokenURL: stageTokenURL,
		},
		Version: version,
	}
	if vaultPath != "" {
		spec.VaultConfig.SecretFilePath = vaultPath
	}
	return spec
}

func TestGetOTelCollectorAuthConfig(t *testing.T) {
	tests := []struct {
		name                  string
		nb                    *nvidiaiov1.NVCFBackend
		envType               nvidiaiov1.EnvType
		expectedClientID      string
		expectedSecretFile    string
		expectedTokenURL      string
		expectedAuthenticator string
	}{
		{
			name:                  "Vault enabled, prod, clientID set → OAuth2, oauth file",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid-prod", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "cid-prod",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, stage, clientID set → OAuth2, stage URL",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid-stage", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeStage,
			expectedClientID:      "cid-stage",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLStage,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, custom SecretFilePath",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "/custom/vault/path")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "cid",
			expectedSecretFile:    "/custom/vault/path/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, version 2.51+ → oauth-client-secrets.env",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "oauth-cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.53.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "oauth-cid",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault enabled, version 2.50 → oauth-client-secrets.env",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "oauth-cid", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.50.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "oauth-cid",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      otelCollectorTokenURLProd,
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorOAuth2Client,
		},
		{
			name:                  "Vault disabled → bearer, empty OAuth client ID",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(false, "", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
		{
			name:                  "Vault absent → bearer, empty OAuth client ID, stage URL",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: nvidiaiov1.NVCFBackendSpecT{}}},
			envType:               nvidiaiov1.EnvTypeStage,
			expectedClientID:      "",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
		{
			name:                  "Vault enabled, empty ClientID → fallback to bearer auth with empty OAuth client ID",
			nb:                    &nvidiaiov1.NVCFBackend{Spec: nvidiaiov1.NVCFBackendSpec{NVCFBackendSpecT: otelAuthSpec(true, "", otelCollectorTokenURLProd, otelCollectorTokenURLStage, "2.53.0", "")}},
			envType:               nvidiaiov1.EnvTypeProd,
			expectedClientID:      "",
			expectedSecretFile:    "/home/nvca/vault-agent/secrets/oauth-client-secrets.env",
			expectedTokenURL:      "",
			expectedAuthenticator: NVCAOTelCollectorAuthenticatorBearerTokenAuth,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := &BackendK8sCache{envType: tt.envType}
			result := bc.getOTelCollectorAuthConfig(tt.nb)
			assert.Equal(t, tt.expectedClientID, result.clientID)
			assert.Equal(t, tt.expectedSecretFile, result.clientSecretFile)
			assert.Equal(t, tt.expectedTokenURL, result.tokenURL)
			assert.Equal(t, tt.expectedAuthenticator, result.authenticator)
		})
	}
}
