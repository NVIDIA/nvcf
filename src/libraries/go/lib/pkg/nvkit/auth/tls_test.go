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

package auth

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

const (
	testCertFile        = "../test/certs/localhost-server.crt"
	testKeyFile         = "../test/certs/localhost-server.fakekey"
	testInvalidCertFile = "invalid-file.crt"
	testInvalidKeyFile  = "invalid-file.key"
)

var (
	testMatchAnyError = fmt.Errorf("helper to match against any error")
)

func TestTLSConfigOptions_Certificate(t *testing.T) {
	type testCase struct {
		desc         string
		option       TLSConfigOptions
		expectedCert *tls.Certificate
		expectedErr  error
	}
	testCases := []testCase{
		{
			desc: "Case: noop when TLS is not enabled",
			option: TLSConfigOptions{
				Enabled: false,
			},
			expectedCert: nil,
			expectedErr:  nil,
		},
		{
			desc: "Case: cert file is missing",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
			},
			expectedCert: nil,
			expectedErr:  errors.ErrCertAndKeyRequired,
		},
		{
			desc: "Case: key file is missing",
			option: TLSConfigOptions{
				Enabled: true,
				KeyFile: testKeyFile,
			},
			expectedCert: nil,
			expectedErr:  errors.ErrCertAndKeyRequired,
		},
		{
			desc: "Case: cert file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testInvalidCertFile,
				KeyFile:  testKeyFile,
			},
			expectedCert: nil,
			expectedErr:  testMatchAnyError,
		},
		{
			desc: "Case: key file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
				KeyFile:  testInvalidKeyFile,
			},
			expectedCert: nil,
			expectedErr:  testMatchAnyError,
		},
		{
			desc: "Valid case: certificate should be loaded with valid options",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
				KeyFile:  testKeyFile,
			},
			expectedCert: &tls.Certificate{},
			expectedErr:  nil,
		},
	}
	for _, tc := range testCases {
		cert, err := tc.option.Certificate()
		if tc.expectedErr == testMatchAnyError {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.Equal(t, tc.expectedErr, err, tc.desc)
		}
		if tc.expectedCert != nil {
			assert.IsType(t, tc.expectedCert, cert, tc.desc)
			assert.NotNil(t, tc.option.Cert, tc.desc)
		} else {
			assert.Nil(t, cert, tc.desc)
		}
	}
}

func TestTLSConfigOptions_LoadClientCAPool(t *testing.T) {
	type testCase struct {
		desc         string
		option       TLSConfigOptions
		expectedPool *x509.CertPool
		expectedErr  error
	}
	testCases := []testCase{
		{
			desc: "Case: noop when TLS is not enabled",
			option: TLSConfigOptions{
				Enabled: false,
			},
			expectedPool: nil,
			expectedErr:  nil,
		},
		{
			desc: "Case: no extra ca cert file is provided",
			option: TLSConfigOptions{
				Enabled: true,
			},
			expectedPool: &x509.CertPool{},
			expectedErr:  nil,
		},
		{
			desc: "Case: invalid ca cert file is provided",
			option: TLSConfigOptions{
				Enabled:           true,
				ClientCACertFiles: []string{testInvalidCertFile},
			},
			expectedPool: nil,
			expectedErr:  testMatchAnyError,
		},
		{
			desc: "Valid case: cert pool should be loaded with valid options",
			option: TLSConfigOptions{
				Enabled:           true,
				ClientCACertFiles: []string{testCertFile},
			},
			expectedPool: &x509.CertPool{},
			expectedErr:  nil,
		},
	}
	for _, tc := range testCases {
		pool, err := tc.option.LoadClientCAPool()
		if tc.expectedErr == testMatchAnyError {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.Equal(t, tc.expectedErr, err, tc.desc)
		}
		if tc.expectedPool != nil {
			assert.IsType(t, tc.expectedPool, pool, tc.desc)
			assert.NotNil(t, tc.option.ClientCAPool, tc.desc)
		} else {
			assert.Nil(t, pool, tc.desc)
		}
	}
}

func TestTLSConfigOptions_LoadRootCAPool(t *testing.T) {
	type testCase struct {
		desc         string
		option       TLSConfigOptions
		expectedPool *x509.CertPool
		expectedErr  error
	}
	testCases := []testCase{
		{
			desc: "Case: noop when TLS is not enabled",
			option: TLSConfigOptions{
				Enabled: false,
			},
			expectedPool: nil,
			expectedErr:  nil,
		},
		{
			desc: "Case: invalid root ca cert file is provided",
			option: TLSConfigOptions{
				Enabled:    true,
				RootCAFile: testInvalidCertFile,
			},
			expectedPool: nil,
			expectedErr:  testMatchAnyError,
		},
		{
			desc: "Valid Case: no root ca cert file provided loads system cert pool",
			option: TLSConfigOptions{
				Enabled: true,
			},
			expectedPool: &x509.CertPool{},
			expectedErr:  nil,
		},
		{
			desc: "Valid case: cert pool should be loaded with valid options",
			option: TLSConfigOptions{
				Enabled:    true,
				RootCAFile: testCertFile,
			},
			expectedPool: &x509.CertPool{},
			expectedErr:  nil,
		},
	}
	for _, tc := range testCases {
		pool, err := tc.option.LoadRootCAPool()
		if tc.expectedErr == testMatchAnyError {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.Equal(t, tc.expectedErr, err, tc.desc)
		}
		if tc.expectedPool != nil {
			sysPool, err := x509.SystemCertPool()
			assert.Nil(t, err, "test error: cannot load system cert pool")
			poolLen := len(sysPool.Subjects())
			if tc.option.RootCAFile != "" {
				poolLen = 1
			}
			assert.IsType(t, tc.expectedPool, pool, tc.desc)
			assert.Equal(t, poolLen, len(pool.Subjects()), tc.desc)
			assert.NotNil(t, tc.option.RootCAPool, tc.desc)
		} else {
			assert.Nil(t, pool, tc.desc)
		}
	}
}

func TestTLSConfigOptions_ServerTLSConfig(t *testing.T) {
	type testCase struct {
		desc           string
		option         TLSConfigOptions
		expectedConfig *tls.Config
		expectedErr    error
	}
	testCases := []testCase{
		{
			desc: "Case: noop when TLS is not enabled",
			option: TLSConfigOptions{
				Enabled: false,
			},
			expectedConfig: nil,
			expectedErr:    nil,
		},
		{
			desc: "Case: cert file is missing",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
			},
			expectedConfig: nil,
			expectedErr:    errors.ErrCertAndKeyRequired,
		},
		{
			desc: "Case: key file is missing",
			option: TLSConfigOptions{
				Enabled: true,
				KeyFile: testKeyFile,
			},
			expectedConfig: nil,
			expectedErr:    errors.ErrCertAndKeyRequired,
		},
		{
			desc: "Case: cert file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testInvalidCertFile,
				KeyFile:  testKeyFile,
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Case: key file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
				KeyFile:  testInvalidKeyFile,
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Case: ca cert file is missing, system cert pool must be loaded",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
				KeyFile:  testKeyFile,
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
		{
			desc: "Case: invalid ca cert file is provided",
			option: TLSConfigOptions{
				Enabled:           true,
				CertFile:          testCertFile,
				KeyFile:           testKeyFile,
				ClientCACertFiles: []string{testInvalidCertFile},
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Valid case: server TLS config should be returned with proper options",
			option: TLSConfigOptions{
				Enabled:           true,
				CertFile:          testCertFile,
				KeyFile:           testKeyFile,
				ClientCACertFiles: []string{testCertFile},
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
	}
	for _, tc := range testCases {
		tlsConfig, err := tc.option.ServerTLSConfig()
		if tc.expectedErr == testMatchAnyError {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.Equal(t, tc.expectedErr, err, tc.desc)
		}
		if tc.expectedConfig != nil {
			assert.Equal(t, 1, len(tlsConfig.Certificates), tc.desc)
			assert.Equal(t, *tc.option.Cert, tlsConfig.Certificates[0], tc.desc)
			assert.NotNil(t, tlsConfig.ClientCAs, tc.desc)
			if tc.option.ClientCACertFiles != nil {
				assert.Equal(t, tc.option.ClientCAPool, tlsConfig.ClientCAs, tc.desc)
			}
		} else {
			assert.Nil(t, tlsConfig, tc.desc)
		}
	}
}

func TestTLSConfigOptions_ClientTLSConfig(t *testing.T) {
	type testCase struct {
		desc           string
		option         TLSConfigOptions
		expectedConfig *tls.Config
		expectedErr    error
	}
	testCases := []testCase{
		{
			desc: "Case: noop when TLS is not enabled",
			option: TLSConfigOptions{
				Enabled: false,
			},
			expectedConfig: nil,
			expectedErr:    nil,
		},
		{
			desc: "Case: cert file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testInvalidCertFile,
				KeyFile:  testKeyFile,
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Case: key file is invalid",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
				KeyFile:  testInvalidKeyFile,
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Case: invalid ca cert file is provided",
			option: TLSConfigOptions{
				Enabled:    true,
				RootCAFile: testInvalidCertFile,
			},
			expectedConfig: nil,
			expectedErr:    testMatchAnyError,
		},
		{
			desc: "Valid case: cert file is missing, should ignore",
			option: TLSConfigOptions{
				Enabled:  true,
				CertFile: testCertFile,
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
		{
			desc: "Valid case: key file is missing, should ignore",
			option: TLSConfigOptions{
				Enabled: true,
				KeyFile: testKeyFile,
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
		{
			desc: "Valid case: no cert/key or client-ca-cert provided",
			option: TLSConfigOptions{
				Enabled: true,
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
		{
			desc: "Valid case: client TLS config should be returned with proper options",
			option: TLSConfigOptions{
				Enabled:            true,
				CertFile:           testCertFile,
				KeyFile:            testKeyFile,
				RootCAFile:         testCertFile,
				InsecureSkipVerify: true,
			},
			expectedConfig: &tls.Config{},
			expectedErr:    nil,
		},
	}
	for _, tc := range testCases {
		tlsConfig, err := tc.option.ClientTLSConfig()
		if tc.expectedErr == testMatchAnyError {
			assert.NotNil(t, err, tc.desc)
		} else {
			assert.Equal(t, tc.expectedErr, err, tc.desc)
		}
		if tc.expectedConfig != nil {
			assert.LessOrEqual(t, len(tlsConfig.Certificates), 1, tc.desc)
			if len(tlsConfig.Certificates) == 1 {
				assert.Equal(t, *tc.option.Cert, tlsConfig.Certificates[0], tc.desc)
			}
			assert.NotNil(t, tlsConfig.RootCAs, tc.desc)
			if tc.option.RootCAFile != "" {
				assert.Equal(t, tc.option.RootCAPool, tlsConfig.RootCAs, tc.desc)
			}
			assert.Equal(t, tc.option.InsecureSkipVerify, tlsConfig.InsecureSkipVerify, tc.desc)
		} else {
			assert.Nil(t, tlsConfig, tc.desc)
		}
	}
}
