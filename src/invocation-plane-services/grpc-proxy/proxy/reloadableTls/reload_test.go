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

package reloadableTls

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeKeyPair writes a fresh self-signed certificate and key into dir and
// returns the certificate DER.
func writeKeyPair(t *testing.T, dir, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return der
}

// swapDataDir points mount/..data at version the way the kubelet's atomic
// writer does: a temporary symlink renamed over the old one.
func swapDataDir(t *testing.T, mount, version string) {
	t.Helper()
	tmp := filepath.Join(mount, "..data_tmp")
	require.NoError(t, os.Symlink(version, tmp))
	require.NoError(t, os.Rename(tmp, filepath.Join(mount, kubernetesDataDir)))
}

func servedCertificate(t *testing.T, conf *tls.Config) []byte {
	t.Helper()
	cert, err := conf.GetCertificate(&tls.ClientHelloInfo{})
	require.NoError(t, err)
	return cert.Certificate[0]
}

func TestReloadsOnKubernetesSecretUpdate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("kqueue reports no event for a renamed-over directory entry; inotify reports a create for ..data")
	}
	mount := t.TempDir()
	first := writeKeyPair(t, filepath.Join(mount, "..2026_01_01_00_00_00.000000001"), "first")
	require.NoError(t, os.Symlink("..2026_01_01_00_00_00.000000001", filepath.Join(mount, kubernetesDataDir)))
	for _, name := range []string{"tls.crt", "tls.key"} {
		require.NoError(t, os.Symlink(filepath.Join(kubernetesDataDir, name), filepath.Join(mount, name)))
	}

	conf, err := NewTLSReloader(filepath.Join(mount, "tls.crt"), filepath.Join(mount, "tls.key"), "", &tls.Config{})
	require.NoError(t, err)
	require.Equal(t, first, servedCertificate(t, conf))

	second := writeKeyPair(t, filepath.Join(mount, "..2026_01_02_00_00_00.000000002"), "second")
	swapDataDir(t, mount, "..2026_01_02_00_00_00.000000002")

	assert.Eventually(t, func() bool {
		return bytes.Equal(second, servedCertificate(t, conf))
	}, 5*time.Second, 20*time.Millisecond, "the renewed certificate was not served after the ..data swap")
}

func TestShouldReload(t *testing.T) {
	watchFiles := map[string]struct{}{"/certs/tls.crt": {}, "/certs/tls.key": {}}
	for _, tc := range []struct {
		name string
		evt  fsnotify.Event
		want bool
	}{
		{"watched file created", fsnotify.Event{Name: "/certs/tls.crt", Op: fsnotify.Create}, true},
		{"kubernetes data symlink swapped", fsnotify.Event{Name: "/certs/..data", Op: fsnotify.Create}, true},
		{"kubernetes temporary symlink", fsnotify.Event{Name: "/certs/..data_tmp", Op: fsnotify.Create}, false},
		{"unrelated file created", fsnotify.Event{Name: "/certs/other", Op: fsnotify.Create}, false},
		{"watched file chmod", fsnotify.Event{Name: "/certs/tls.crt", Op: fsnotify.Chmod}, false},
		{"kubernetes data symlink removed", fsnotify.Event{Name: "/certs/..data", Op: fsnotify.Remove}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldReload(tc.evt, watchFiles))
		})
	}
}
