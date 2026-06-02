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

package ca

import (
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"sync"
)

const (
	EnvCAFile = "NVCF_PROXY_CA_FILE" // colon-separated path(s) to PEM file(s)
	EnvCAPEM  = "NVCF_PROXY_CA_PEM"  // inline PEM material (one or more concatenated certs)
)

var (
	mu        sync.Mutex
	once      sync.Once
	cachePool *x509.CertPool
	cacheErr  error
	extraPEMs [][]byte
	sealed    bool
)

// SetExtraCAs registers additional PEM-encoded CA certificates to be included
// in the trust pool. Must be called before the first call to ProxyCAs;
// calling it after ProxyCAs has been invoked will panic.
// This is intended for managed deployments that supply NVIDIA-specific roots
// programmatically (e.g. via a private //go:embed package).
func SetExtraCAs(pemBytes ...[]byte) {
	mu.Lock()
	defer mu.Unlock()
	if sealed {
		panic("ca: SetExtraCAs called after ProxyCAs was already invoked")
	}
	extraPEMs = append(extraPEMs, pemBytes...)
}

// ProxyCAs returns an x509.CertPool that starts from the system trust store
// and appends any additional CAs supplied via:
//   - SetExtraCAs (programmatic injection, for managed wrappers)
//   - NVCF_PROXY_CA_FILE env var (colon-separated file paths)
//   - NVCF_PROXY_CA_PEM env var (inline PEM bytes)
//
// If none of these are set, the pool contains only system roots.
// The pool is built once and cached for the lifetime of the process.
func ProxyCAs() (*x509.CertPool, error) {
	once.Do(func() {
		mu.Lock()
		sealed = true
		mu.Unlock()

		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}

		for _, pem := range extraPEMs {
			if !pool.AppendCertsFromPEM(pem) {
				cacheErr = fmt.Errorf("ca: no valid PEM certs in programmatically supplied bytes")
				return
			}
		}

		if paths := os.Getenv(EnvCAFile); paths != "" {
			for _, p := range strings.Split(paths, ":") {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				pem, err := os.ReadFile(p)
				if err != nil {
					cacheErr = fmt.Errorf("ca: read %s=%s: %w", EnvCAFile, p, err)
					return
				}
				if !pool.AppendCertsFromPEM(pem) {
					cacheErr = fmt.Errorf("ca: no valid PEM certs in %s", p)
					return
				}
			}
		}

		if inline := os.Getenv(EnvCAPEM); inline != "" {
			if !pool.AppendCertsFromPEM([]byte(inline)) {
				cacheErr = fmt.Errorf("ca: no valid PEM certs in $%s", EnvCAPEM)
				return
			}
		}

		cachePool = pool
	})
	return cachePool, cacheErr
}
