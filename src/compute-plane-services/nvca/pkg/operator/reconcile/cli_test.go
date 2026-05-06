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
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cli "github.com/urfave/cli/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/mirror"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"
)

func TestNewOperatorCommand(t *testing.T) {
	cmd := NewOperatorCommand()
	assert.NotNil(t, cmd)

	fakeExit := func(int) {}
	p := PatchOSExit(t, fakeExit)
	t.Cleanup(p.Unpatch)

	app := &cli.App{
		Name:    cmd.Name,
		Usage:   cmd.Usage,
		Version: "test",
		Flags:   cmd.Flags,
		Action:  cmd.Action,
	}

	dc, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Millisecond))
	t.Cleanup(cancel)
	ctx := core.NewDefaultContext(dc)
	app.RunContext(ctx, []string{cmd.Name})
}

func TestDecodeRR(t *testing.T) {
	def := getDefaultAgentRR()

	// happy path
	input := getDefaultWebhookRR()
	b, _ := json.Marshal(input)
	b64 := base64.StdEncoding.EncodeToString(b)
	got := decodeRR(b64, def)
	assert.Equal(t, input.Limits.Cpu().String(), got.Limits.Cpu().String())

	// empty string -> default
	got = decodeRR("", def)
	assert.Equal(t, def.Limits.Cpu().String(), got.Limits.Cpu().String())

	// invalid base64 -> default
	got = decodeRR("@@@", def)
	assert.Equal(t, def.Limits.Cpu().String(), got.Limits.Cpu().String())
}

func TestDecodeTolerations(t *testing.T) {
	input := []corev1.Toleration{{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}
	b, err := json.Marshal(input)
	require.NoError(t, err)

	got, err := decodeTolerations(base64.StdEncoding.EncodeToString(b))
	require.NoError(t, err)
	assert.Equal(t, input, got)

	got, err = decodeTolerations("")
	require.NoError(t, err)
	assert.Nil(t, got)

	_, err = decodeTolerations("@@@")
	assert.Error(t, err)

	_, err = decodeTolerations(base64.StdEncoding.EncodeToString([]byte(`{}`)))
	assert.Error(t, err)
}

func TestDecodeImagePullSecrets_StructFormat(t *testing.T) {
	// Test with the exact struct format used in the function
	type secret struct {
		Name string `json:"name"`
	}

	testCases := []struct {
		name     string
		secrets  []secret
		expected []corev1.LocalObjectReference
	}{
		{
			name:     "single secret with struct",
			secrets:  []secret{{Name: "my-secret"}},
			expected: []corev1.LocalObjectReference{{Name: "my-secret"}},
		},
		{
			name: "multiple secrets with struct",
			secrets: []secret{
				{Name: "first-secret"},
				{Name: "second-secret"},
			},
			expected: []corev1.LocalObjectReference{{Name: "first-secret"}, {Name: "second-secret"}},
		},
		{
			name: "mixed valid and empty with struct",
			secrets: []secret{
				{Name: "valid"},
				{Name: ""},
				{Name: "also-valid"},
			},
			expected: []corev1.LocalObjectReference{{Name: "valid"}, {Name: "also-valid"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.secrets)
			require.NoError(t, err)
			b64 := base64.StdEncoding.EncodeToString(b)

			got, err := mirror.DecodeImagePullSecrets(b64)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestDecodeEnvVarsMap(t *testing.T) {
	testCases := []struct {
		name      string
		input     string
		expected  map[string]string
		expectErr bool
	}{
		{
			name:      "empty string returns nil",
			input:     "",
			expected:  nil,
			expectErr: false,
		},
		{
			name:      "invalid base64 returns error",
			input:     "@@@",
			expected:  nil,
			expectErr: true,
		},
		{
			name: "invalid JSON returns error",
			input: func() string {
				return base64.StdEncoding.EncodeToString([]byte("not-json"))
			}(),
			expected:  nil,
			expectErr: true,
		},
		{
			name: "valid JSON with single key",
			input: func() string {
				data := map[string]string{"LOG_LEVEL": "debug"}
				b, _ := json.Marshal(data)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected:  map[string]string{"LOG_LEVEL": "debug"},
			expectErr: false,
		},
		{
			name: "valid JSON with multiple keys",
			input: func() string {
				data := map[string]string{
					"LOG_LEVEL":   "debug",
					"CUSTOM_FLAG": "enabled",
					"MY_VAR":      "value",
				}
				b, _ := json.Marshal(data)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected: map[string]string{
				"LOG_LEVEL":   "debug",
				"CUSTOM_FLAG": "enabled",
				"MY_VAR":      "value",
			},
			expectErr: false,
		},
		{
			name: "valid JSON with empty map",
			input: func() string {
				data := map[string]string{}
				b, _ := json.Marshal(data)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			expected:  map[string]string{},
			expectErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := decodeEnvVarsMap(tc.input)
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGetDefaultAgentRR(t *testing.T) {
	rr := getDefaultAgentRR()

	// Test CPU limits
	expectedCPULimit := resource.MustParse("1000m")
	assert.True(t, rr.Limits[corev1.ResourceCPU].Equal(expectedCPULimit),
		"Expected CPU limit %s, got %s", expectedCPULimit.String(), rr.Limits.Cpu().String())

	// Test Memory limits
	expectedMemoryLimit := resource.MustParse("4Gi")
	assert.True(t, rr.Limits[corev1.ResourceMemory].Equal(expectedMemoryLimit),
		"Expected Memory limit %s, got %s", expectedMemoryLimit.String(), rr.Limits.Memory().String())

	// Test CPU requests
	expectedCPURequest := resource.MustParse("100m")
	assert.True(t, rr.Requests[corev1.ResourceCPU].Equal(expectedCPURequest),
		"Expected CPU request %s, got %s", expectedCPURequest.String(), rr.Requests.Cpu().String())

	// Test Memory requests
	expectedMemoryRequest := resource.MustParse("200Mi")
	assert.True(t, rr.Requests[corev1.ResourceMemory].Equal(expectedMemoryRequest),
		"Expected Memory request %s, got %s", expectedMemoryRequest.String(), rr.Requests.Memory().String())

	// Test that both Limits and Requests are not nil
	assert.NotNil(t, rr.Limits, "Limits should not be nil")
	assert.NotNil(t, rr.Requests, "Requests should not be nil")

	// Test that we have exactly 2 resource types in each
	assert.Len(t, rr.Limits, 2, "Should have exactly 2 limit types")
	assert.Len(t, rr.Requests, 2, "Should have exactly 2 request types")
}

func TestGetDefaultWebhookRR(t *testing.T) {
	rr := getDefaultWebhookRR()

	// Test CPU limits
	expectedCPULimit := resource.MustParse("200m")
	assert.True(t, rr.Limits[corev1.ResourceCPU].Equal(expectedCPULimit),
		"Expected CPU limit %s, got %s", expectedCPULimit.String(), rr.Limits.Cpu().String())

	// Test Memory limits
	expectedMemoryLimit := resource.MustParse("200Mi")
	assert.True(t, rr.Limits[corev1.ResourceMemory].Equal(expectedMemoryLimit),
		"Expected Memory limit %s, got %s", expectedMemoryLimit.String(), rr.Limits.Memory().String())

	// Test CPU requests
	expectedCPURequest := resource.MustParse("50m")
	assert.True(t, rr.Requests[corev1.ResourceCPU].Equal(expectedCPURequest),
		"Expected CPU request %s, got %s", expectedCPURequest.String(), rr.Requests.Cpu().String())

	// Test Memory requests
	expectedMemoryRequest := resource.MustParse("50Mi")
	assert.True(t, rr.Requests[corev1.ResourceMemory].Equal(expectedMemoryRequest),
		"Expected Memory request %s, got %s", expectedMemoryRequest.String(), rr.Requests.Memory().String())

	// Test that both Limits and Requests are not nil
	assert.NotNil(t, rr.Limits, "Limits should not be nil")
	assert.NotNil(t, rr.Requests, "Requests should not be nil")

	// Test that we have exactly 2 resource types in each
	assert.Len(t, rr.Limits, 2, "Should have exactly 2 limit types")
	assert.Len(t, rr.Requests, 2, "Should have exactly 2 request types")
}

func TestDefaultResourceRequirementsComparison(t *testing.T) {
	agentRR := getDefaultAgentRR()
	webhookRR := getDefaultWebhookRR()

	// Verify agent has higher resource requirements than webhook
	assert.True(t, agentRR.Limits.Cpu().Cmp(*webhookRR.Limits.Cpu()) > 0,
		"Agent CPU limit should be higher than webhook CPU limit")

	assert.True(t, agentRR.Limits.Memory().Cmp(*webhookRR.Limits.Memory()) > 0,
		"Agent Memory limit should be higher than webhook Memory limit")

	assert.True(t, agentRR.Requests.Cpu().Cmp(*webhookRR.Requests.Cpu()) > 0,
		"Agent CPU request should be higher than webhook CPU request")

	assert.True(t, agentRR.Requests.Memory().Cmp(*webhookRR.Requests.Memory()) > 0,
		"Agent Memory request should be higher than webhook Memory request")
}

func TestGetDefaultOTelCollectorRR(t *testing.T) {
	rr := getDefaultOTelCollectorRR()

	// Test CPU limits
	expectedCPULimit := resource.MustParse("1000m")
	assert.True(t, rr.Limits[corev1.ResourceCPU].Equal(expectedCPULimit),
		"Expected CPU limit %s, got %s", expectedCPULimit.String(), rr.Limits.Cpu().String())

	// Test Memory limits
	expectedMemoryLimit := resource.MustParse("1Gi")
	assert.True(t, rr.Limits[corev1.ResourceMemory].Equal(expectedMemoryLimit),
		"Expected Memory limit %s, got %s", expectedMemoryLimit.String(), rr.Limits.Memory().String())

	// Test CPU requests
	expectedCPURequest := resource.MustParse("200m")
	assert.True(t, rr.Requests[corev1.ResourceCPU].Equal(expectedCPURequest),
		"Expected CPU request %s, got %s", expectedCPURequest.String(), rr.Requests.Cpu().String())

	// Test Memory requests
	expectedMemoryRequest := resource.MustParse("256Mi")
	assert.True(t, rr.Requests[corev1.ResourceMemory].Equal(expectedMemoryRequest),
		"Expected Memory request %s, got %s", expectedMemoryRequest.String(), rr.Requests.Memory().String())

	// Test that both Limits and Requests are not nil
	assert.NotNil(t, rr.Limits, "Limits should not be nil")
	assert.NotNil(t, rr.Requests, "Requests should not be nil")

	// Test that we have exactly 2 resource types in each
	assert.Len(t, rr.Limits, 2, "Should have exactly 2 limit types")
	assert.Len(t, rr.Requests, 2, "Should have exactly 2 request types")
}

func TestOperatorCommand_NVCAFlags(t *testing.T) {
	cmd := NewOperatorCommand()
	assert.NotNil(t, cmd)

	// Test that our new NVCA-specific flags are present
	flagNames := make(map[string]bool)
	for _, flag := range cmd.Flags {
		for _, name := range flag.Names() {
			flagNames[name] = true
		}
	}

	// Verify new flags exist
	assert.True(t, flagNames["nvca-cache-mount-options-enabled"], "nvca-cache-mount-options-enabled flag should exist")
	assert.True(t, flagNames["nvca-cache-mount-options"], "nvca-cache-mount-options flag should exist")
	assert.True(t, flagNames["nvca-worker-degradation-period"], "nvca-worker-degradation-period flag should exist")
	assert.True(t, flagNames["additional-image-pull-secrets-b64"], "additional-image-pull-secrets-b64 flag should exist")
	assert.True(t, flagNames["agent-override-env-vars-json-b64"], "agent-override-env-vars-json-b64 flag should exist")

	// Verify OTel collector flags exist
	assert.True(t, flagNames["otel-collector-image-repo"], "otel-collector-image-repo flag should exist")
	assert.True(t, flagNames["otel-collector-image-tag"], "otel-collector-image-tag flag should exist")
	assert.True(t, flagNames["otel-collector-resources-b64"], "otel-collector-resources-b64 flag should exist")

	// Verify existing flags still exist
	assert.True(t, flagNames["enable-gxcache"], "enable-gxcache flag should still exist")
	assert.True(t, flagNames["nca-id"], "nca-id flag should still exist")
	assert.True(t, flagNames["cluster-name"], "cluster-name flag should still exist")
}

func TestOperatorCommand_NVCAFlagDefaults(t *testing.T) {
	cmd := NewOperatorCommand()

	var cacheMountOptionsEnabledFlag *cli.BoolFlag
	var cacheMountOptionsFlag *cli.StringFlag
	var workerDegradationPeriodFlag *cli.DurationFlag
	var additionalImagePullSecretsB64Flag *cli.StringFlag
	var otelCollectorImageRepoFlag *cli.StringFlag
	var otelCollectorImageTagFlag *cli.StringFlag
	var otelCollectorResourcesB64Flag *cli.StringFlag

	// Find our specific flags
	for _, flag := range cmd.Flags {
		switch f := flag.(type) {
		case *cli.BoolFlag:
			if f.Name == "nvca-cache-mount-options-enabled" {
				cacheMountOptionsEnabledFlag = f
			}
		case *cli.StringFlag:
			switch f.Name {
			case "nvca-cache-mount-options":
				cacheMountOptionsFlag = f
			case "additional-image-pull-secrets-b64":
				additionalImagePullSecretsB64Flag = f
			case "otel-collector-image-repo":
				otelCollectorImageRepoFlag = f
			case "otel-collector-image-tag":
				otelCollectorImageTagFlag = f
			case "otel-collector-resources-b64":
				otelCollectorResourcesB64Flag = f
			}
		case *cli.DurationFlag:
			if f.Name == "nvca-worker-degradation-period" {
				workerDegradationPeriodFlag = f
			}
		}
	}

	// Verify flags were found and have correct defaults
	assert.NotNil(t, cacheMountOptionsEnabledFlag, "nvca-cache-mount-options-enabled flag should be found")
	assert.Equal(t, false, cacheMountOptionsEnabledFlag.Value, "nvca-cache-mount-options-enabled should default to false")

	assert.NotNil(t, cacheMountOptionsFlag, "nvca-cache-mount-options flag should be found")
	assert.Equal(t, "", cacheMountOptionsFlag.Value, "nvca-cache-mount-options should default to empty")

	assert.NotNil(t, workerDegradationPeriodFlag, "nvca-worker-degradation-period flag should be found")
	assert.Equal(t, time.Duration(0), workerDegradationPeriodFlag.Value, "nvca-worker-degradation-period should default to 0")

	assert.NotNil(t, additionalImagePullSecretsB64Flag, "additional-image-pull-secrets-b64 flag should be found")
	assert.Equal(t, "", additionalImagePullSecretsB64Flag.Value, "additional-image-pull-secrets-b64 should default to empty")

	// Verify OTel collector flags
	assert.NotNil(t, otelCollectorImageRepoFlag, "otel-collector-image-repo flag should be found")
	assert.Equal(t, defaultOTelCollectorImageRepo, otelCollectorImageRepoFlag.Value,
		"otel-collector-image-repo should have correct default")

	assert.NotNil(t, otelCollectorImageTagFlag, "otel-collector-image-tag flag should be found")
	assert.Equal(t, defaultOTelCollectorImageTag, otelCollectorImageTagFlag.Value,
		"otel-collector-image-tag should have correct default")

	assert.NotNil(t, otelCollectorResourcesB64Flag, "otel-collector-resources-b64 flag should be found")
	assert.Equal(t, "", otelCollectorResourcesB64Flag.Value,
		"otel-collector-resources-b64 should default to empty")
}

// Helper function to create a CLI context with all required flags
func createTestCLIContext(ctx context.Context, overrides map[string]interface{}) *cli.Context {
	app := &cli.App{}
	set := flag.NewFlagSet("test", 0)

	// Default values
	defaults := map[string]interface{}{
		"log-level":                         "debug",
		"cluster-source":                    "ngc-managed",
		"nca-id":                            "test-nca-id",
		"cluster-name":                      "test-cluster",
		"ngc-service-key":                   "test-key",
		"agent-resources-b64":               "",
		"webhook-resources-b64":             "",
		"additional-image-pull-secrets-b64": "",
		"otel-exporter":                     "none",
		"otel-lightstep-service-name":       "",
		"otel-lighstep-access-token":        "",
		"kubeconfig":                        "",
		"k8s-version-override":              "",
		"listen":                            ":8000",
		"listen-admin":                      "127.0.0.1:8001",
		"system-namespace":                  "nvca-operator",
		"priority-class-name":               "",
		"ngc-service-key-file":              "",
		"node-selector-key":                 "node.kubernetes.io/instance-type",
		"node-selector-value":               "",
		"ngc-api-url":                       "https://api.ngc.nvidia.com",
		"cluster-id":                        "",
		"nvca-cluster-api-refresh-interval": 5 * time.Minute,
		"nvca-image-repo":                   "nvcr.io/nvidia/nvcf-byoc/nvca",
		"nvca-run-as-userid":                int64(1000770002),
		"nvca-run-as-groupid":               int64(1000770002),
		"nvca-gxcache-namespace":            "gxcache",
		"nvca-helm-repository-prefix":       "",
		"enable-gxcache":                    false,
		"ddcs-ip-allowlist":                 "",
		"k8s-cluster-network-cidrs":         "",
		"nvca-cache-mount-options-enabled":  false,
		"nvca-cache-mount-options":          "",
		"nvca-worker-degradation-period":    time.Duration(0),
		"otel-collector-image-repo":         defaultOTelCollectorImageRepo,
		"otel-collector-image-tag":          defaultOTelCollectorImageTag,
		"otel-collector-resources-b64":      "",
		"agent-override-env-vars-json-b64":  "",
	}

	// Apply overrides
	for k, v := range overrides {
		defaults[k] = v
	}

	// Set up flags based on type
	for k, v := range defaults {
		switch val := v.(type) {
		case string:
			set.String(k, val, "")
		case bool:
			set.Bool(k, val, "")
		case int64:
			set.Int64(k, val, "")
		case time.Duration:
			set.Duration(k, val, "")
		}
	}

	c := cli.NewContext(app, set, nil)
	c.Context = ctx
	return c
}

func TestDoAction_InvalidLogLevel(t *testing.T) {
	// Test log level validation without calling doAction to avoid starting agent infrastructure
	ctx := context.Background()
	log := core.GetLogger(ctx)

	err := core.SetLevel(log, "invalid-level")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "level")
}

func TestDoAction_InvalidClusterSource(t *testing.T) {
	// Test cluster source validation without calling doAction to avoid starting agent infrastructure
	_, err := nvcaoptypes.ValidateClusterSource("invalid-source")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cluster source")
}

// Test individual components of doAction without full execution

// Test cluster source validation logic separately
func TestDoAction_ClusterSourceValidation(t *testing.T) {
	testCases := []struct {
		name          string
		clusterSource string
		expectError   bool
	}{
		{"Valid NGC Managed", "ngc-managed", false},
		{"Valid Helm Managed", "helm-managed", false},
		{"Invalid Source", "invalid-source", true},
		{"Empty Source", "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := nvcaoptypes.ValidateClusterSource(tc.clusterSource)
			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid cluster source")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetDefaultNewTokenFetcher(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		ngcServiceKeyFile string
		clusterSource     nvcaoptypes.ClusterSource
		setupFunc         func(t *testing.T) string // returns the actual file path to use
		expectError       bool
		expectEmptyToken  bool
		description       string
	}{
		{
			name:              "self-managed cluster returns empty token fetcher",
			ngcServiceKeyFile: "/some/path/key",
			clusterSource:     nvcaoptypes.ClusterSourceSelfHosted,
			setupFunc: func(t *testing.T) string {
				return "/some/path/key" // file doesn't need to exist for self-managed
			},
			expectError:      false,
			expectEmptyToken: true,
			description:      "Self-managed clusters should return an empty token fetcher",
		},
		{
			name:              "ngc-managed cluster with valid key file",
			ngcServiceKeyFile: "",
			clusterSource:     nvcaoptypes.ClusterSourceNGCManaged,
			setupFunc: func(t *testing.T) string {
				// Create a temporary file with a mock NGC service key
				tmpDir := t.TempDir()
				keyFile := filepath.Join(tmpDir, "ngcServiceKey")
				err := os.WriteFile(keyFile, []byte("mock-ngc-service-key"), 0600)
				require.NoError(t, err)
				return keyFile
			},
			expectError:      false,
			expectEmptyToken: false,
			description:      "NGC-managed clusters with valid key file should return a working token fetcher",
		},
		{
			name:              "ngc-managed cluster with empty key file path",
			ngcServiceKeyFile: "",
			clusterSource:     nvcaoptypes.ClusterSourceNGCManaged,
			setupFunc: func(t *testing.T) string {
				return "" // empty path
			},
			expectError:      true,
			expectEmptyToken: false,
			description:      "NGC-managed clusters without key file should return an error",
		},
		{
			name:              "ngc-managed cluster with non-existent key file",
			ngcServiceKeyFile: "",
			clusterSource:     nvcaoptypes.ClusterSourceNGCManaged,
			setupFunc: func(t *testing.T) string {
				return "/non/existent/path/ngcServiceKey"
			},
			expectError:      true, // KeyFileFetcher validates file exists during creation
			expectEmptyToken: false,
			description:      "NGC-managed clusters with non-existent key file should return an error",
		},
		{
			name:              "ngc-managed cluster with directory instead of key file",
			ngcServiceKeyFile: "",
			clusterSource:     nvcaoptypes.ClusterSourceNGCManaged,
			setupFunc: func(t *testing.T) string {
				// Create a directory with the same name as what should be a file
				tmpDir := t.TempDir()
				keyFile := filepath.Join(tmpDir, "ngcServiceKey")
				err := os.Mkdir(keyFile, 0755) // Create directory instead of file
				require.NoError(t, err)
				return keyFile
			},
			expectError:      true, // KeyFileFetcher should fail when trying to open a directory as a file
			expectEmptyToken: false,
			description:      "NGC-managed clusters with directory instead of key file should return an error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test environment
			actualKeyFile := tt.setupFunc(t)

			// Call the function under test
			fetcher, err := getDefaultNewTokenFetcher(ctx, actualKeyFile, tt.clusterSource)

			// Check error expectations
			if tt.expectError {
				assert.Error(t, err, tt.description)
				assert.Nil(t, fetcher, "Fetcher should be nil when error is expected")
				return
			}

			// If no error expected, fetcher should be returned
			require.NoError(t, err, tt.description)
			require.NotNil(t, fetcher, "Fetcher should not be nil when no error is expected")

			// Test token fetching behavior
			token, fetchErr := fetcher.FetchToken(ctx)

			if tt.expectEmptyToken {
				assert.NoError(t, fetchErr, "Empty token fetcher should not return fetch error")
				assert.Empty(t, token, "Self-managed cluster should return empty token")
			} else {
				// For NGC-managed clusters, we test that the fetcher was created successfully
				// The actual token fetching may fail based on file permissions/existence,
				// but that's expected behavior for the KeyFileFetcher
				assert.NotNil(t, fetcher, "Fetcher should be created for NGC-managed clusters")

				// Only test successful token fetch if the file exists and is readable
				if actualKeyFile != "" && actualKeyFile != "/non/existent/path/ngcServiceKey" {
					// Check if file is readable
					if _, statErr := os.Stat(actualKeyFile); statErr == nil {
						if fileInfo, _ := os.Stat(actualKeyFile); fileInfo.Mode().Perm()&0400 != 0 {
							// File exists and is readable
							assert.NoError(t, fetchErr, "Token fetch should succeed for readable key file")
							assert.NotEmpty(t, token, "Token should not be empty for valid key file")
						}
					}
				}
			}
		})
	}
}

// Test resource requirements decoding logic separately
func TestDoAction_ResourceRequirementsDecodingLogic(t *testing.T) {
	testCases := []struct {
		name        string
		input       string
		expectError bool
	}{
		{
			"Valid Base64 JSON",
			func() string {
				rr := corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2000m"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				}
				b, _ := json.Marshal(rr)
				return base64.StdEncoding.EncodeToString(b)
			}(),
			false,
		},
		{"Empty String", "", false},                                                        // Should use defaults
		{"Invalid Base64", "invalid-base64-@@@", false},                                    // Should use defaults
		{"Invalid JSON", base64.StdEncoding.EncodeToString([]byte("invalid-json")), false}, // Should use defaults
	}

	defaultRR := getDefaultAgentRR()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := decodeRR(tc.input, defaultRR)
			assert.NotNil(t, result)
			// Should always return a valid ResourceRequirements (either decoded or default)
			assert.NotNil(t, result.Limits)
			assert.NotNil(t, result.Requests)
		})
	}
}

func TestGetDefaultNewTokenFetcher_ClusterSourceValidation(t *testing.T) {
	ctx := context.Background()

	// Test all valid cluster source types
	validSources := []nvcaoptypes.ClusterSource{
		nvcaoptypes.ClusterSourceNGCManaged,
		nvcaoptypes.ClusterSourceSelfHosted,
	}

	for _, source := range validSources {
		t.Run(string(source), func(t *testing.T) {
			var keyFile string
			if source == nvcaoptypes.ClusterSourceNGCManaged {
				// Create a temporary key file for NGC-managed
				tmpDir := t.TempDir()
				keyFile = filepath.Join(tmpDir, "ngcServiceKey")
				err := os.WriteFile(keyFile, []byte("test-key"), 0600)
				require.NoError(t, err)
			}

			fetcher, err := getDefaultNewTokenFetcher(ctx, keyFile, source)

			if source == nvcaoptypes.ClusterSourceSelfHosted {
				assert.NoError(t, err)
				assert.NotNil(t, fetcher)

				// Verify empty token for self-managed
				token, fetchErr := fetcher.FetchToken(ctx)
				assert.NoError(t, fetchErr)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, fetcher)

				// For NGC-managed with valid file, token fetch should work
				token, fetchErr := fetcher.FetchToken(ctx)
				assert.NoError(t, fetchErr)
				assert.Equal(t, "test-key", token)
			}
		})
	}
}

// Test AgentOptions construction from CLI flags
func TestDoAction_AgentOptionsConstruction(t *testing.T) {
	ctx := context.Background()
	workloadTolerationsJSON, err := json.Marshal([]corev1.Toleration{{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}})
	require.NoError(t, err)
	agentTolerationsJSON, err := json.Marshal([]corev1.Toleration{{
		Key:      "nvidia.com/test-agent",
		Operator: corev1.TolerationOpEqual,
		Value:    "true",
		Effect:   corev1.TaintEffectNoSchedule,
	}})
	require.NoError(t, err)
	c := createTestCLIContext(ctx, map[string]interface{}{
		"nca-id":                           "test-nca-123",
		"cluster-name":                     "test-cluster-name",
		"cluster-source":                   "helm-managed",
		"nvca-cache-mount-options-enabled": true,
		"nvca-cache-mount-options":         "rw,noatime",
		"nvca-worker-degradation-period":   90 * time.Minute,
		"workload-tolerations-b64":         base64.StdEncoding.EncodeToString(workloadTolerationsJSON),
		"agent-tolerations-b64":            base64.StdEncoding.EncodeToString(agentTolerationsJSON),
		"enable-gxcache":                   true,
		"kubeconfig":                       "/path/to/kubeconfig",
		"k8s-version-override":             "v1.28.0",
	})

	// Test the cluster source validation step
	clusterSource, err := nvcaoptypes.ValidateClusterSource(c.String("cluster-source"))
	assert.NoError(t, err)
	assert.Equal(t, nvcaoptypes.ClusterSourceHelmManaged, clusterSource)

	// Test resource requirements decoding
	defAgentRR := getDefaultAgentRR()
	defWebhookRR := getDefaultWebhookRR()
	agentRR := decodeRR(c.String("agent-resources-b64"), defAgentRR)
	webhookRR := decodeRR(c.String("webhook-resources-b64"), defWebhookRR)
	workloadTolerations, err := decodeTolerations(c.String("workload-tolerations-b64"))
	require.NoError(t, err)
	agentTolerations, err := decodeTolerations(c.String("agent-tolerations-b64"))
	require.NoError(t, err)

	// Verify that we can construct AgentOptions with the parsed values
	opts := &AgentOptions{
		NCAID:                        c.String("nca-id"),
		KubeConfigPath:               c.String("kubeconfig"),
		K8sVersionOverride:           c.String("k8s-version-override"),
		ClusterName:                  c.String("cluster-name"),
		ClusterSource:                clusterSource,
		EnableGXCache:                c.Bool("enable-gxcache"),
		AgentResources:               agentRR,
		WebhookResources:             webhookRR,
		NVCACacheMountOptionsEnabled: c.Bool("nvca-cache-mount-options-enabled"),
		NVCACacheMountOptions:        c.String("nvca-cache-mount-options"),
		NVCAWorkerDegradationPeriod:  c.Duration("nvca-worker-degradation-period"),
		NVCAWorkloadTolerations:      workloadTolerations,
		NVCAAgentTolerations:         agentTolerations,
	}

	// Verify the options were set correctly
	assert.Equal(t, "test-nca-123", opts.NCAID)
	assert.Equal(t, "test-cluster-name", opts.ClusterName)
	assert.Equal(t, nvcaoptypes.ClusterSourceHelmManaged, opts.ClusterSource)
	assert.Equal(t, "/path/to/kubeconfig", opts.KubeConfigPath)
	assert.Equal(t, "v1.28.0", opts.K8sVersionOverride)
	assert.True(t, opts.EnableGXCache)
	assert.True(t, opts.NVCACacheMountOptionsEnabled)
	assert.Equal(t, "rw,noatime", opts.NVCACacheMountOptions)
	assert.Equal(t, 90*time.Minute, opts.NVCAWorkerDegradationPeriod)
	assert.Equal(t, []corev1.Toleration{{
		Key:      "nvidia.com/test-workload",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}}, opts.NVCAWorkloadTolerations)
	assert.Equal(t, []corev1.Toleration{{
		Key:      "nvidia.com/test-agent",
		Operator: corev1.TolerationOpEqual,
		Value:    "true",
		Effect:   corev1.TaintEffectNoSchedule,
	}}, opts.NVCAAgentTolerations)
	assert.NotNil(t, opts.AgentResources)
	assert.NotNil(t, opts.WebhookResources)

}

func TestDoAction_AgentOptionsWithImagePullSecrets(t *testing.T) {
	ctx := context.Background()

	// Create base64 encoded JSON for image pull secrets
	secrets := []map[string]string{
		{"name": "secret-one"},
		{"name": "secret-two"},
	}
	b, err := json.Marshal(secrets)
	require.NoError(t, err)
	b64Secrets := base64.StdEncoding.EncodeToString(b)

	c := createTestCLIContext(ctx, map[string]interface{}{
		"nca-id":                            "test-nca-123",
		"cluster-name":                      "test-cluster-name",
		"cluster-source":                    "ngc-managed",
		"additional-image-pull-secrets-b64": b64Secrets,
	})

	// Test the cluster source validation step
	clusterSource, err := nvcaoptypes.ValidateClusterSource(c.String("cluster-source"))
	assert.NoError(t, err)
	assert.Equal(t, nvcaoptypes.ClusterSourceNGCManaged, clusterSource)

	// Test image pull secrets decoding
	additionalSecrets, err := mirror.DecodeImagePullSecrets(c.String("additional-image-pull-secrets-b64"))
	require.NoError(t, err)

	// Verify that we can construct AgentOptions with the parsed values
	opts := &AgentOptions{
		NCAID:                      c.String("nca-id"),
		ClusterName:                c.String("cluster-name"),
		ClusterSource:              clusterSource,
		AdditionalImagePullSecrets: additionalSecrets,
	}

	// Verify the options were set correctly
	assert.Equal(t, "test-nca-123", opts.NCAID)
	assert.Equal(t, "test-cluster-name", opts.ClusterName)
	assert.Equal(t, nvcaoptypes.ClusterSourceNGCManaged, opts.ClusterSource)
	assert.NotNil(t, opts.AdditionalImagePullSecrets)
	assert.Len(t, opts.AdditionalImagePullSecrets, 2)
	assert.Equal(t, []corev1.LocalObjectReference{
		{Name: "secret-one"},
		{Name: "secret-two"},
	}, opts.AdditionalImagePullSecrets)
}
func TestDoAction_AgentOptionsWithOTelCollector(t *testing.T) {
	ctx := context.Background()

	// Create base64 encoded JSON for OTel collector resources
	customResources := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2000m"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
	b, err := json.Marshal(customResources)
	require.NoError(t, err)
	b64Resources := base64.StdEncoding.EncodeToString(b)

	c := createTestCLIContext(ctx, map[string]interface{}{
		"nca-id":                       "test-nca-123",
		"cluster-name":                 "test-cluster-name",
		"cluster-source":               "ngc-managed",
		"otel-collector-image-repo":    "custom.registry.io/otel-collector",
		"otel-collector-image-tag":     "1.0.0",
		"otel-collector-resources-b64": b64Resources,
	})

	// Test OTel collector resource requirements decoding
	defOTelCollectorRR := getDefaultOTelCollectorRR()
	otelCollectorRR := decodeRR(c.String("otel-collector-resources-b64"), defOTelCollectorRR)

	// Verify custom resources were decoded correctly
	assert.True(t, otelCollectorRR.Limits.Cpu().Equal(resource.MustParse("2000m")),
		"OTel collector CPU limit should be custom value")
	assert.True(t, otelCollectorRR.Limits.Memory().Equal(resource.MustParse("2Gi")),
		"OTel collector Memory limit should be custom value")
	assert.True(t, otelCollectorRR.Requests.Cpu().Equal(resource.MustParse("500m")),
		"OTel collector CPU request should be custom value")
	assert.True(t, otelCollectorRR.Requests.Memory().Equal(resource.MustParse("512Mi")),
		"OTel collector Memory request should be custom value")

	// Verify we can construct AgentOptions with OTel collector values
	opts := &AgentOptions{
		NCAID:                  c.String("nca-id"),
		ClusterName:            c.String("cluster-name"),
		OTelCollectorImageRepo: c.String("otel-collector-image-repo"),
		OTelCollectorImageTag:  c.String("otel-collector-image-tag"),
		OTelCollectorResources: otelCollectorRR,
	}

	// Verify the options were set correctly
	assert.Equal(t, "test-nca-123", opts.NCAID)
	assert.Equal(t, "test-cluster-name", opts.ClusterName)
	assert.Equal(t, "custom.registry.io/otel-collector", opts.OTelCollectorImageRepo)
	assert.Equal(t, "1.0.0", opts.OTelCollectorImageTag)
	assert.NotNil(t, opts.OTelCollectorResources)
}

func TestDoAction_OTelCollectorDefaultResources(t *testing.T) {
	ctx := context.Background()

	// Create context with empty otel-collector-resources-b64 (should use defaults)
	c := createTestCLIContext(ctx, map[string]interface{}{
		"otel-collector-resources-b64": "",
	})

	// Test that empty string falls back to defaults
	defOTelCollectorRR := getDefaultOTelCollectorRR()
	otelCollectorRR := decodeRR(c.String("otel-collector-resources-b64"), defOTelCollectorRR)

	// Verify default resources are used
	assert.True(t, otelCollectorRR.Limits.Cpu().Equal(resource.MustParse("1000m")),
		"OTel collector CPU limit should be default value")
	assert.True(t, otelCollectorRR.Limits.Memory().Equal(resource.MustParse("1Gi")),
		"OTel collector Memory limit should be default value")
	assert.True(t, otelCollectorRR.Requests.Cpu().Equal(resource.MustParse("200m")),
		"OTel collector CPU request should be default value")
	assert.True(t, otelCollectorRR.Requests.Memory().Equal(resource.MustParse("256Mi")),
		"OTel collector Memory request should be default value")
}

func TestTokenFetcherAdapter(t *testing.T) {
	ctx := context.Background()

	// Test the tokenFetcherAdapter type used in getDefaultNewTokenFetcher
	expectedToken := "test-token"
	expectedError := assert.AnError

	// Test successful token fetch
	t.Run("successful fetch", func(t *testing.T) {
		adapter := tokenFetcherAdapter(func(_ context.Context) (string, error) {
			return expectedToken, nil
		})

		token, err := adapter.FetchToken(ctx)
		assert.NoError(t, err)
		assert.Equal(t, expectedToken, token)
	})

	// Test error during fetch
	t.Run("error during fetch", func(t *testing.T) {
		adapter := tokenFetcherAdapter(func(_ context.Context) (string, error) {
			return "", expectedError
		})

		token, err := adapter.FetchToken(ctx)
		assert.Error(t, err)
		assert.Equal(t, expectedError, err)
		assert.Empty(t, token)
	})

	// Test context passing
	t.Run("context is passed correctly", func(t *testing.T) {
		contextReceived := false
		adapter := tokenFetcherAdapter(func(receivedCtx context.Context) (string, error) {
			contextReceived = receivedCtx == ctx
			return "token", nil
		})

		_, err := adapter.FetchToken(ctx)
		assert.NoError(t, err)
		assert.True(t, contextReceived, "Context should be passed to the underlying function")
	})
}
