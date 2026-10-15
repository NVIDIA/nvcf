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

package trustbundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRootCertPEM = `-----BEGIN CERTIFICATE-----
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

const testRootFingerprint = "sha256:9a7814909424061a68756ee5c26aa1a1491b8d20a7b813fb24fa7e73b2fa1c93"

func TestMergeFilesAppendsValidatedBundleAndVerifiesFingerprint(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.pem")
	trustPath := filepath.Join(dir, "trust.pem")
	outPath := filepath.Join(dir, "out", "ca-certificates.crt")
	require.NoError(t, os.WriteFile(systemPath, []byte("system-root\n"), 0644))
	require.NoError(t, os.WriteFile(trustPath, []byte(testRootCertPEM), 0644))

	err := MergeFiles(InstallOptions{
		SystemBundlePath:    systemPath,
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outPath,
		ExpectedFingerprint: testRootFingerprint,
	})

	require.NoError(t, err)
	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(got), "system-root\n\n"))
	assert.Contains(t, string(got), testRootCertPEM)
}

func TestMergeFilesAllowsMissingSystemBundle(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outPath := filepath.Join(dir, "out", "ca-certificates.crt")
	require.NoError(t, os.WriteFile(trustPath, []byte(testRootCertPEM), 0644))

	err := MergeFiles(InstallOptions{
		SystemBundlePath:    filepath.Join(dir, "missing-system.pem"),
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outPath,
		ExpectedFingerprint: testRootFingerprint,
	})

	require.NoError(t, err)
	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, testRootCertPEM+"\n", string(got))
}

func TestMergeFilesRejectsFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outPath := filepath.Join(dir, "out.pem")
	require.NoError(t, os.WriteFile(trustPath, []byte(testRootCertPEM), 0644))

	err := MergeFiles(InstallOptions{
		SystemBundlePath:    filepath.Join(dir, "missing-system.pem"),
		TrustBundlePath:     trustPath,
		OutputBundlePath:    outPath,
		ExpectedFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "fingerprint")
}

func TestMergeFilesRequiresExpectedFingerprint(t *testing.T) {
	dir := t.TempDir()
	trustPath := filepath.Join(dir, "trust.pem")
	outPath := filepath.Join(dir, "out.pem")
	require.NoError(t, os.WriteFile(trustPath, []byte(testRootCertPEM), 0644))

	err := MergeFiles(InstallOptions{
		SystemBundlePath: filepath.Join(dir, "missing-system.pem"),
		TrustBundlePath:  trustPath,
		OutputBundlePath: outPath,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected fingerprint")
}

func TestValidatePEMRejectsNonCertificateBlocks(t *testing.T) {
	err := ValidatePEM("-----BEGIN PRIVATE KEY-----\nYWJj\n-----END PRIVATE KEY-----\n")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "PEM block type")
}

func TestValidatePEMRejectsEmptyBundle(t *testing.T) {
	err := ValidatePEM(" \n\t")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no CERTIFICATE PEM blocks found")
}

func TestValidatePEMRejectsGarbageAroundCertificate(t *testing.T) {
	for _, input := range []string{
		"garbage\n" + testRootCertPEM,
		testRootCertPEM + "\ntrailing garbage",
		testRootCertPEM + "\nnot-a-pem-block\n" + testRootCertPEM,
	} {
		err := ValidatePEM(input)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected non-whitespace data")
	}
}

func TestFingerprintPEMSupportsDualRootBundles(t *testing.T) {
	one, err := FingerprintPEM(testRootCertPEM)
	require.NoError(t, err)

	two, err := FingerprintPEM(testRootCertPEM + "\n" + testRootCertPEM)
	require.NoError(t, err)

	assert.Equal(t, one, two, "duplicate certs should not change the canonical bundle fingerprint")
}
