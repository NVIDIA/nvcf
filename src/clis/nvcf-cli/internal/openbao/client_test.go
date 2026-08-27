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
	"os/exec"
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

func TestReadPKICertificatePEMRetriesMalformedResponse(t *testing.T) {
	responses := []string{
		"Internal Server Error",
		`{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`,
	}
	attempt := 0

	got, err := readPKICertificatePEM(nil, len(responses), 0, func(context.Context) (string, error) {
		response := responses[attempt]
		attempt++
		return response, nil
	})

	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
	assert.Equal(t, len(responses), attempt)
}

func TestReadPKICertificatePEMRetriesEmptyResponse(t *testing.T) {
	responses := []string{
		"",
		`{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`,
	}
	attempt := 0

	got, err := readPKICertificatePEM(context.Background(), len(responses), 0, func(context.Context) (string, error) {
		response := responses[attempt]
		attempt++
		return response, nil
	})

	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
	assert.Equal(t, len(responses), attempt)
}

func TestReadPKICertificatePEMDoesNotRetryOpenBaoError(t *testing.T) {
	attempt := 0

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (string, error) {
		attempt++
		return `{"errors":["permission denied"]}`, nil
	})

	require.Error(t, err)
	assert.Equal(t, 1, attempt)
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
