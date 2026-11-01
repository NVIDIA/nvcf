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

package clients

import (
	"context"
	"net"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
)

const (
	testAuthnServer  = "authn.test.com"
	testClientID     = "test-client-id"
	testClientSecret = "test-client-secret"
	testClientName   = "test-client"
	testAddr         = "http://test-addr"
)

func TestGRPCClientConfig_AddClientFlags(t *testing.T) {
	var nilCfg *GRPCClientConfig
	testCommand := &cobra.Command{}
	result := nilCfg.AddClientFlags(testCommand, testClientName)
	assert.False(t, result)
	assert.False(t, testCommand.Flags().HasFlags())

	testCfg := &GRPCClientConfig{}
	result = testCfg.AddClientFlags(nil, testClientName)
	assert.False(t, result)
	assert.False(t, testCommand.Flags().HasFlags())

	result = testCfg.AddClientFlags(testCommand, "")
	assert.False(t, result)
	assert.False(t, testCommand.Flags().HasFlags())

	result = testCfg.AddClientFlags(testCommand, testClientName)
	assert.True(t, result)
	assert.True(t, testCommand.Flags().HasFlags())
}

func TestGRPCClientConfig_DialOptions(t *testing.T) {
	// Simulate invalid client config with invalid tls options
	testInvalidClientCfg := &GRPCClientConfig{BaseClientConfig: &BaseClientConfig{TLS: auth.TLSConfigOptions{Enabled: true, RootCAFile: "some-file"}}}
	opts, err := testInvalidClientCfg.DialOptions()
	assert.NotNil(t, err)
	assert.Nil(t, opts)

	// Test valid client config
	testValidClientCfg := &GRPCClientConfig{
		BaseClientConfig: &BaseClientConfig{
			Addr: testAddr,
			// Tracer: stdopentracing.GlobalTracer(),
			TLS: auth.TLSConfigOptions{Enabled: true},
		},
	}
	opts, err = testValidClientCfg.DialOptions()
	assert.Nil(t, err)
	// Should include tls, retry, keepalive, tracer option, and stats handler
	assert.Equal(t, 5, len(opts))

	// Test valid client config with authn
	testAuthnConfig := &auth.AuthnConfig{
		OIDCConfig: &auth.ProviderConfig{
			Host:         testAuthnServer,
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			Scopes:       []string{"TestScope"},
		},
	}

	testValidClientCfg = &GRPCClientConfig{
		BaseClientConfig: &BaseClientConfig{
			Addr:     testAddr,
			TLS:      auth.TLSConfigOptions{Enabled: true},
			AuthnCfg: testAuthnConfig,
		},
	}
	opts, err = testValidClientCfg.DialOptions()
	assert.Nil(t, err)
	// Should include tls, retry, tracer, authn, keepalive option, and stats handler
	assert.Equal(t, 6, len(opts))

	// Test valid client config with dial option overrides
	testCfgWithOverrides := &GRPCClientConfig{
		DialOptOverrides: []grpc.DialOption{grpc.EmptyDialOption{}},
	}
	opts, err = testCfgWithOverrides.DialOptions()
	assert.Nil(t, err)
	assert.Equal(t, 1, len(opts))
}

func TestGRPCClientConfig_Dial(t *testing.T) {
	// Simulate invalid client config with invalid tls options
	testInvalidClientCfg := &GRPCClientConfig{BaseClientConfig: &BaseClientConfig{TLS: auth.TLSConfigOptions{Enabled: true, RootCAFile: "some-file"}}}
	conn, err := testInvalidClientCfg.Dial()
	assert.NotNil(t, err)
	assert.Nil(t, conn)

	// Simulate connection failure by returning a permanent connection error
	testInvalidAddrClientCfg := &GRPCClientConfig{
		BaseClientConfig: &BaseClientConfig{Addr: testAddr},
		DialOptOverrides: []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
			grpc.FailOnNonTempDialError(true),
			grpc.WithContextDialer(func(
				ctx context.Context, s string,
			) (net.Conn, error) {
				return nil, failFastError{}
			}),
		},
	}
	conn, err = testInvalidAddrClientCfg.Dial()
	assert.NotNil(t, err)
	assert.Nil(t, conn)

	// Test valid client config case
	testValidClientCfg := &GRPCClientConfig{BaseClientConfig: &BaseClientConfig{Addr: "grpc.testing.google.fr:443"}}
	conn, err = testValidClientCfg.Dial()
	assert.Nil(t, err)
	assert.NotNil(t, conn)
}

type failFastError struct{}

func (failFastError) Error() string   { return "test permanent connection error" }
func (failFastError) Temporary() bool { return false }
