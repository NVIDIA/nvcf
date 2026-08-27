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

package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const openBaoTestCertPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func TestRootCAPEMFromOpenBaoResponseAcceptsJSONCertificate(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"data": map[string]string{"certificate": openBaoTestCertPEM},
	})
	require.NoError(t, err)

	got, err := rootCAPEMFromOpenBaoResponse(string(body))
	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
}

func TestRootCAPEMFromOpenBaoResponseAcceptsRawPEM(t *testing.T) {
	got, err := rootCAPEMFromOpenBaoResponse(openBaoTestCertPEM)
	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
}

func TestRootCAPEMFromOpenBaoResponseMapsMissingPKIToSentinel(t *testing.T) {
	_, err := rootCAPEMFromOpenBaoResponse(`{"errors":["no handler for route \"services/all/pki/root/cert/ca\""]}`)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPKICertificateNotFound))
}

func TestKubectlBaseArgsIncludesContext(t *testing.T) {
	c := NewClient(&Config{KubeconfigPath: "/tmp/kubeconfig", KubeContext: "cp-context"}, nil)
	assert.Equal(t, []string{"kubectl", "--kubeconfig", "/tmp/kubeconfig", "--context", "cp-context"}, c.kubectlBaseArgs())
}

func TestFilterKubectlOutputPreservesPEMBlockWithKubectlNoise(t *testing.T) {
	c := NewClient(&Config{}, nil)
	got := c.filterKubectlOutput("pod \"openbao-pki-root-ca\" deleted\n" + openBaoTestCertPEM + "pod \"openbao-pki-root-ca\" deleted\n")
	assert.Equal(t, openBaoTestCertPEM[:len(openBaoTestCertPEM)-1], got)
}

func TestFilterKubectlOutputDropsLowercaseAttachFallbackAfterResponse(t *testing.T) {
	c := NewClient(&Config{}, nil)
	response := `{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`
	tests := map[string]string{
		"normal attach": response + "\npod \"openbao-pki-root-ca\" deleted\n",
		"log fallback": response +
			"\nwarning: couldn't attach to pod/openbao-pki-root-ca, falling back to streaming logs\n" +
			"pod \"openbao-pki-root-ca\" deleted\n",
	}

	for name, output := range tests {
		t.Run(name, func(t *testing.T) {
			filtered := c.filterKubectlOutput(output)
			got, err := rootCAPEMFromOpenBaoResponse(filtered)
			require.NoError(t, err)
			assert.Equal(t, openBaoTestCertPEM, got)
		})
	}
}

func TestRootCAPEMFromOpenBaoResponsePreservesCertificateErrors(t *testing.T) {
	tests := map[string]string{
		"malformed response":  "not-json",
		"OpenBao error":       `{"errors":["permission denied"]}`,
		"missing certificate": `{"data":{}}`,
	}

	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := rootCAPEMFromOpenBaoResponse(response)
			require.Error(t, err)
		})
	}
}

func TestKubectlOutputMetadataDoesNotExposeCertificate(t *testing.T) {
	metadata := kubectlOutputMetadata(openBaoTestCertPEM)

	assert.Equal(t, "59 bytes", metadata)
	assert.NotContains(t, metadata, "CERTIFICATE")
}

func TestExecuteKubectlRunPreservesCommandError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	c := NewClient(&Config{}, nil)
	_, err := c.executeKubectlRun(context.Background(), "test", nil)
	require.Error(t, err)

	var execErr *exec.Error
	require.ErrorAs(t, err, &execErr)
}

func TestReadPKICertificatePEMUsesPublicEndpointWithoutRootToken(t *testing.T) {
	testDir := t.TempDir()
	commandLog := filepath.Join(testDir, "kubectl.log")
	kubectlPath := filepath.Join(testDir, "kubectl")
	kubectlScript := `#!/bin/sh
printf '%s\n' "$*" >> "$KUBECTL_COMMAND_LOG"
case " $* " in
  *" get secret "*) exit 91 ;;
  *" X-Vault-Token: "*) exit 92 ;;
esac
printf '%s\n' '{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}'
`
	require.NoError(t, os.WriteFile(kubectlPath, []byte(kubectlScript), 0o755))
	t.Setenv("PATH", testDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KUBECTL_COMMAND_LOG", commandLog)

	client := NewClient(&Config{
		OpenBaoURL:        "http://openbao-openbao.nvcf.svc.cluster.local:8200",
		OpenBaoNamespace:  "openbao",
		OpenBaoSecretName: "openbao-root-token",
		ClusterNamespace:  "nvcf",
		UtilityImage:      "curlimages/curl:latest",
	}, nil)

	got, err := client.ReadPKICertificatePEM(context.Background(), "services/all/pki/root")
	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)

	logBody, err := os.ReadFile(commandLog)
	require.NoError(t, err)
	commands := string(logBody)
	assert.NotContains(t, commands, " get secret ")
	assert.NotContains(t, commands, "X-Vault-Token")
	assert.Contains(t, commands, "/v1/services/all/pki/root/cert/ca")
}
