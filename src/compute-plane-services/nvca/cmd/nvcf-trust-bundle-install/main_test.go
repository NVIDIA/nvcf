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

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunInstallsBundle(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.pem")
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out", "ca-certificates.crt")
	require.NoError(t, os.WriteFile(systemPath, []byte("system-root\n"), 0644))
	require.NoError(t, os.WriteFile(trustPath, []byte(trustbundleTestRootCertPEM), 0644))

	var stderr bytes.Buffer
	code := run([]string{
		"--system-bundle", systemPath,
		"--trust-bundle", trustPath,
		"--output-bundle", outputPath,
		"--expected-fingerprint", trustbundleTestRootFingerprint,
	}, &stderr)

	require.Equal(t, 0, code, stderr.String())
	got, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	assert.Contains(t, string(got), "system-root")
	assert.Contains(t, string(got), "BEGIN CERTIFICATE")
}

func TestRunFailsForInvalidBundle(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out.pem")
	require.NoError(t, os.WriteFile(trustPath, []byte("not pem"), 0644))

	var stderr bytes.Buffer
	code := run([]string{
		"--system-bundle", filepath.Join(dir, "missing.pem"),
		"--trust-bundle", trustPath,
		"--output-bundle", outputPath,
		"--expected-fingerprint", trustbundleTestRootFingerprint,
	}, &stderr)

	require.Equal(t, 1, code)
	assert.Contains(t, stderr.String(), "install trust bundle")
}

func TestRunFailsWithoutExpectedFingerprint(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outputPath := filepath.Join(dir, "out.pem")
	require.NoError(t, os.WriteFile(trustPath, []byte(trustbundleTestRootCertPEM), 0644))

	var stderr bytes.Buffer
	code := run([]string{
		"--system-bundle", filepath.Join(dir, "missing.pem"),
		"--trust-bundle", trustPath,
		"--output-bundle", outputPath,
	}, &stderr)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "--expected-fingerprint is required")
}

const trustbundleTestRootCertPEM = `-----BEGIN CERTIFICATE-----
MIIDFzCCAf+gAwIBAgIUaNWvYBOx1GWnVct8jQamwHfprvMwDQYJKoZIhvcNAQEL
BQAwGzEZMBcGA1UEAwwQTlZDRiBUZXN0IFJvb3QgQTAeFw0yNjA0MzAyMTUyMjha
Fw0yNzA0MzAyMTUyMjhaMBsxGTAXBgNVBAMMEE5WQ0YgVGVzdCBSb290IEEwggEi
MA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQC7E+dEUss30Im2ixgsEXQZVdYA
lxz5ppRa5J1Olmbttb2upCjmdO/OxdkyP2Y1YA/pBrN4k98OGhxx9GJLVCtRL0ix
34tBAFDOn3RM2iM9oZvpbKIX+n1oUR8DXO6RiQ4Y4dLs3RLhfXrf6V9tL/YmYL7X
TLDaElPCcbrf6traGhNdOrwk9+GCtJP5CZsRePssPg9EmAxei2CerAYRtFHl8oEd
yTcK44LOR10Mo3wbz2axqWXjILG++l6o3Vw1SqN4x4GLBmeLNVE5Lkh8MOOsNbKj
rsi5dL5X6SI3J0/DkqjNRrbbNXLjt0lLOsq9ioIlf4aTzR+Ng9Uc2p/gsTfDAgMB
AAGjUzBRMB0GA1UdDgQWBBRZVSjXpLkvKxM2PAGRWiXYa8rSUzAfBgNVHSMEGDAW
gBRZVSjXpLkvKxM2PAGRWiXYa8rSUzAPBgNVHRMBAf8EBTADAQH/MA0GCSqGSIb3
DQEBCwUAA4IBAQCLv2LwHts5FnhafOWL/8lO5p5G0G8aL25lCL+RdqNoTGVIRRJV
f4RQQyGGbERYNaRdvNosh9u1aHzdGhi0i8oEW1N1TTyS6SmmP3/xMoJp3aL5E3AN
Ey9Naentws7yn+x4jxlyVqIecmH/LyiWpNNcKWXEGsDHJ9QQTNXicKiNwKNabKIv
RNOvPCpX1WFgj+rp2l3ahYACUzYbVGvuJXrF4fSawK0T/RWbXc7dkK68se0CGcuL
qswu4hFDV8na6EuT2ThxFXEuRb/OtIZdzfsTw0r9OixIlP1wGmzzQdZ9wi8mfe7i
LuQqFOQcWPEX70Ig+I6SWsb7VB6f0hZ2VGvA
-----END CERTIFICATE-----`

const trustbundleTestRootFingerprint = "sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93"
