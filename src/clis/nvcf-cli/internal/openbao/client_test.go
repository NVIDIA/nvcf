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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	responses := []pkiCertificateHTTPResponse{
		{StatusCode: http.StatusOK, ContentType: "text/plain", Body: "not-json"},
		{StatusCode: http.StatusOK, ContentType: "application/json", Body: `{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`},
	}
	attempt := 0

	got, err := readPKICertificatePEM(context.Background(), len(responses), 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		response := responses[attempt]
		attempt++
		return response, nil
	})

	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
	assert.Equal(t, len(responses), attempt)
}

func TestReadPKICertificatePEMRetriesEmptyResponse(t *testing.T) {
	responses := []pkiCertificateHTTPResponse{
		{StatusCode: http.StatusOK},
		{StatusCode: http.StatusOK, ContentType: "application/json", Body: `{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`},
	}
	attempt := 0

	got, err := readPKICertificatePEM(context.Background(), len(responses), 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
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

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		attempt++
		return pkiCertificateHTTPResponse{
			StatusCode:  http.StatusOK,
			ContentType: "application/json",
			Body:        `{"errors":["permission denied"]}`,
		}, nil
	})

	require.Error(t, err)
	assert.Equal(t, 1, attempt)
}

func TestReadPKICertificatePEMRetriesServerError(t *testing.T) {
	responses := []pkiCertificateHTTPResponse{
		{StatusCode: http.StatusServiceUnavailable, ContentType: "text/plain", Body: "Internal Server Error"},
		{StatusCode: http.StatusOK, ContentType: "application/json", Body: `{"data":{"certificate":"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"}}`},
	}
	attempt := 0

	got, err := readPKICertificatePEM(context.Background(), len(responses), 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		response := responses[attempt]
		attempt++
		return response, nil
	})

	require.NoError(t, err)
	assert.Equal(t, openBaoTestCertPEM, got)
	assert.Equal(t, len(responses), attempt)
}

func TestReadPKICertificatePEMReportsServerErrorAfterRetries(t *testing.T) {
	attempt := 0

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		attempt++
		return pkiCertificateHTTPResponse{
			StatusCode:  http.StatusServiceUnavailable,
			ContentType: "text/plain; charset=utf-8",
			Body:        "Internal Server Error",
		}, nil
	})

	require.Error(t, err)
	assert.Equal(t, 3, attempt)
	assert.ErrorContains(t, err, "HTTP 503")
	assert.ErrorContains(t, err, `content type "text/plain; charset=utf-8"`)
	assert.ErrorContains(t, err, "Internal Server Error")
	assert.NotContains(t, err.Error(), "invalid character")
}

func TestReadPKICertificatePEMDoesNotRetryClientError(t *testing.T) {
	attempt := 0

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		attempt++
		return pkiCertificateHTTPResponse{
			StatusCode:  http.StatusForbidden,
			ContentType: "application/json",
			Body:        `{"errors":["permission denied"]}`,
		}, nil
	})

	require.Error(t, err)
	assert.Equal(t, 1, attempt)
	assert.ErrorContains(t, err, "HTTP 403")
	assert.ErrorContains(t, err, "permission denied")
}

func TestReadPKICertificatePEMReportsUnexpectedSuccessStatus(t *testing.T) {
	attempt := 0

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		attempt++
		return pkiCertificateHTTPResponse{StatusCode: http.StatusNoContent}, nil
	})

	require.Error(t, err)
	assert.Equal(t, 1, attempt)
	assert.ErrorContains(t, err, "HTTP 204")
}

func TestReadPKICertificatePEMPreservesMissingPKIError(t *testing.T) {
	attempt := 0

	_, err := readPKICertificatePEM(context.Background(), 3, 0, func(context.Context) (pkiCertificateHTTPResponse, error) {
		attempt++
		return pkiCertificateHTTPResponse{
			StatusCode:  http.StatusNotFound,
			ContentType: "application/json",
			Body:        `{"errors":["no handler for route services/all/pki/root/cert/ca"]}`,
		}, nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPKICertificateNotFound)
	assert.Equal(t, 1, attempt)
}

func TestPKICertificateHTTPResponseFromKubectlOutput(t *testing.T) {
	c := NewClient(&Config{}, nil)
	output := "Internal Server Error\nupstream unavailable\n" +
		curlHTTPStatusMarker + "503\n" +
		curlHTTPContentTypeMarker + "text/plain\n" +
		`pod "openbao-pki-root-ca" deleted` + "\n"

	response, err := pkiCertificateHTTPResponseFromOutput(c.filterKubectlOutput(output))

	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Equal(t, "text/plain", response.ContentType)
	assert.Equal(t, "Internal Server Error\nupstream unavailable", response.Body)
}

func TestPKICertificateHTTPErrorBoundsResponseBody(t *testing.T) {
	err := pkiCertificateHTTPError(pkiCertificateHTTPResponse{
		StatusCode: http.StatusBadGateway,
		Body:       strings.Repeat("x", maxOpenBaoHTTPErrorBody+100),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), strings.Repeat("x", maxOpenBaoHTTPErrorBody))
	assert.NotContains(t, err.Error(), strings.Repeat("x", maxOpenBaoHTTPErrorBody+1))
	assert.True(t, strings.HasSuffix(err.Error(), "..."))
}

func TestPKICertificateHTTPErrorOmitsCertificateBody(t *testing.T) {
	err := pkiCertificateHTTPError(pkiCertificateHTTPResponse{
		StatusCode: http.StatusBadGateway,
		Body:       openBaoTestCertPEM,
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "<certificate response omitted>")
	assert.NotContains(t, err.Error(), "BEGIN CERTIFICATE")
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
printf '%s\n' '__NVCF_HTTP_STATUS__:200'
printf '%s\n' '__NVCF_HTTP_CONTENT_TYPE__:application/json'
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
	assert.Contains(t, commands, "--write-out")
}
