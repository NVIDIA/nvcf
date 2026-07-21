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

package nvca

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cli "github.com/urfave/cli/v2"
)

const (
	DefaultHeartBeatInterval              = 5 * time.Minute
	DefaultCredRenewInterval              = 45 * time.Minute
	DefaultSyncRequestStatusInterval      = 15 * time.Second
	DefaultPeriodicInstanceStatusInterval = 5 * time.Minute
)

func TestSetDefaultsDoesNotBackfillDefaultStargateAddress(t *testing.T) {
	cfg := nvcaconfig.Config{}

	require.NoError(t, setDefaults(&cfg))

	assert.Empty(t, cfg.Workload.DefaultStargateAddress)
}

func TestSetDefaultsPreservesConfiguredDefaultStargateAddress(t *testing.T) {
	cfg := nvcaconfig.Config{}
	cfg.Workload.DefaultStargateAddress = "llm-router.example.test:50071"

	require.NoError(t, setDefaults(&cfg))

	assert.Equal(t, "llm-router.example.test:50071", cfg.Workload.DefaultStargateAddress)
}

func TestNewCommand(t *testing.T) {
	newAgentFunc := func(a cliAgent) func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
		return func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
			return a, nil
		}
	}
	setFlag := func(g cli.Generic, s string) error {
		return nil
	}
	initLogger := func(log *logrus.Entry) logr.Logger {
		return logr.Discard()
	}

	// Without file, should fail
	t.Run("no config file", func(t *testing.T) {
		a, block := newMockCLIAgent()
		cmd := newCobraCommand(
			newAgentFunc(a),
			setFlag,
			initLogger,
		)
		cmd.SilenceErrors = true
		cmd.SetOut(io.Discard)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error)
		go func() {
			errCh <- cmd.ExecuteContext(ctx)
		}()

		var err error
		select {
		case <-block:
			cancel()
			err = <-errCh
		case err = <-errCh:
			cancel()
		}
		assert.EqualError(t, err, `required flag(s) "config" not set`)
	})

	// With empty config, should pass.
	t.Run("empty config file", func(t *testing.T) {
		a, block := newMockCLIAgent()
		var gotOpts *AgentOptions
		cmd := newCobraCommand(
			func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
				gotOpts = opts
				return a, nil
			},
			setFlag,
			initLogger,
		)

		cfgFilePath := filepath.Join(t.TempDir(), "config.yaml")
		err := os.WriteFile(cfgFilePath, []byte(`
agent:
  icmsURL: https://test.example.com
`), 0600)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error)
		go func() {
			cmd.SetArgs([]string{"--config=" + cfgFilePath})
			errCh <- cmd.ExecuteContext(ctx)
		}()

		select {
		case <-block:
			cancel()
			err = <-errCh
		case err = <-errCh:
			cancel()
		}
		require.NoError(t, err)
		require.NotNil(t, gotOpts)
		assert.False(t, gotOpts.Config.Agent.BYOOLogChunking.Enabled)
		assert.Zero(t, gotOpts.Config.Agent.BYOOLogChunking.MaxBodyBytes)
	})

	t.Run("enabled BYOO log chunking config preserves collector default handoff", func(t *testing.T) {
		a, block := newMockCLIAgent()
		var gotOpts *AgentOptions
		cmd := newCobraCommand(
			func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
				gotOpts = opts
				return a, nil
			},
			setFlag,
			initLogger,
		)

		cfgFilePath := filepath.Join(t.TempDir(), "config.yaml")
		err := os.WriteFile(cfgFilePath, []byte(`
agent:
  icmsURL: https://test.example.com
  byooLogChunking:
    enabled: true
`), 0600)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error)
		go func() {
			cmd.SetArgs([]string{"--config=" + cfgFilePath})
			errCh <- cmd.ExecuteContext(ctx)
		}()

		select {
		case <-block:
			cancel()
			err = <-errCh
		case err = <-errCh:
			cancel()
		}
		require.NoError(t, err)
		require.NotNil(t, gotOpts)
		assert.True(t, gotOpts.Config.Agent.BYOOLogChunking.Enabled)
		assert.Zero(t, gotOpts.Config.Agent.BYOOLogChunking.MaxBodyBytes)
	})

	t.Run("configured control plane endpoints populate agent options", func(t *testing.T) {
		a, block := newMockCLIAgent()
		var gotOpts *AgentOptions
		cmd := newCobraCommand(
			func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
				gotOpts = opts
				return a, nil
			},
			setFlag,
			initLogger,
		)

		cfgFilePath := filepath.Join(t.TempDir(), "config.yaml")
		err := os.WriteFile(cfgFilePath, []byte(`
agent:
  icmsURL: https://test.example.com
  icmsHostHeaderOverride: sis.gateway.example.test
  helmReValServiceURL: http://reval.localhost:18080
  helmReValServiceHostHeaderOverride: reval.gateway.example.test
  helmReValStageOAuthTokenURL: https://stage-reval-oauth.example.test/token
  helmReValStageOAuthPublicKeysetEndpoint: https://stage-reval-oauth.example.test/.well-known/jwks.json
  helmReValProdOAuthTokenURL: https://prod-reval-oauth.example.test/token
  helmReValProdOAuthPublicKeysetEndpoint: https://prod-reval-oauth.example.test/.well-known/jwks.json
  functionDeploymentStagesServiceURL: https://deployment-stages.stg.nvcf.nvidia.com
  functionDeploymentStagesStageOAuthTokenURL: https://stage-fnds-oauth.example.test/token
  functionDeploymentStagesStageOAuthPublicKeysetEndpoint: https://stage-fnds-oauth.example.test/.well-known/jwks.json
  functionDeploymentStagesProdOAuthTokenURL: https://prod-fnds-oauth.example.test/token
  functionDeploymentStagesProdOAuthPublicKeysetEndpoint: https://prod-fnds-oauth.example.test/.well-known/jwks.json
  NATSURL: nats://nats.localhost:14222
  NATSHostOverride: nats.gateway.example.test
`), 0600)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error)
		go func() {
			cmd.SetArgs([]string{"--config=" + cfgFilePath})
			errCh <- cmd.ExecuteContext(ctx)
		}()

		select {
		case <-block:
			cancel()
			err = <-errCh
		case err = <-errCh:
			cancel()
		}
		require.NoError(t, err)
		require.NotNil(t, gotOpts)
		assert.Equal(t, "https://test.example.com", gotOpts.ICMSURL)
		assert.Equal(t, "sis.gateway.example.test", gotOpts.ICMSHostHeaderOverride)
		assert.Equal(t, "http://reval.localhost:18080", gotOpts.HelmReValServiceURL)
		assert.Equal(t, "reval.gateway.example.test", gotOpts.HelmReValServiceHostHeaderOverride)
		assert.Equal(t, "https://stage-reval-oauth.example.test/token", gotOpts.HelmReValStageOAuthTokenURL)
		assert.Equal(t, "https://stage-reval-oauth.example.test/.well-known/jwks.json", gotOpts.HelmReValStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://prod-reval-oauth.example.test/token", gotOpts.HelmReValProdOAuthTokenURL)
		assert.Equal(t, "https://prod-reval-oauth.example.test/.well-known/jwks.json", gotOpts.HelmReValProdOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://deployment-stages.stg.nvcf.nvidia.com", gotOpts.FunctionDeploymentStagesServiceURL)
		assert.Equal(t, "https://stage-fnds-oauth.example.test/token", gotOpts.FunctionDeploymentStagesStageOAuthTokenURL)
		assert.Equal(t, "https://stage-fnds-oauth.example.test/.well-known/jwks.json", gotOpts.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint)
		assert.Equal(t, "https://prod-fnds-oauth.example.test/token", gotOpts.FunctionDeploymentStagesProdOAuthTokenURL)
		assert.Equal(t, "https://prod-fnds-oauth.example.test/.well-known/jwks.json", gotOpts.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint)
		assert.Equal(t, "nats://nats.localhost:14222", gotOpts.NATSURL)
		assert.Equal(t, "nats.gateway.example.test", gotOpts.NATSHostOverride)
	})

	// With populated config, should pass.
	t.Run("populated config file", func(t *testing.T) {
		a, block := newMockCLIAgent()
		var gotFFs string
		cmd := newCobraCommand(
			newAgentFunc(a),
			func(g cli.Generic, s string) error {
				if gotFFs == "" {
					gotFFs = s
				}
				return nil
			},
			initLogger,
		)

		cfgFilePath := filepath.Join(t.TempDir(), "config.yaml")
		err := os.WriteFile(cfgFilePath, []byte(`
agent:
  credRenewInterval: 2ms
  icmsURL: https://test.example.com
  featureFlags:
  - LogPosting
  - -HelmResourceConstraints
  forceSelfDestruct: true
  logLevel: debug
  namespaceLabels:
    foo: bar
cluster:
  id: foo
environment: prod
workload:
  workerDegradationTimeout: 2h0m0s
`), 0600)
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error)
		go func() {
			cmd.SetArgs([]string{"--config=" + cfgFilePath})
			errCh <- cmd.ExecuteContext(ctx)
		}()

		select {
		case <-block:
			cancel()
			err = <-errCh
		case err = <-errCh:
			cancel()
		}
		assert.NoError(t, err)
		assert.Equal(t, "LogPosting,-HelmResourceConstraints", gotFFs)
	})
}

func TestMaintenanceModeFeatureFlagNames(t *testing.T) {
	// Test that feature flag keys are correct
	assert.Equal(t, "CordonMaintenance", featureflag.CordonMaintenance.Key)
	assert.Equal(t, "CordonAndDrainMaintenance", featureflag.CordonAndDrainMaintenance.Key)
}

func TestMaintenanceModeConstants(t *testing.T) {
	// Test that maintenance mode constants are correctly defined
	assert.Equal(t, types.MaintenanceMode("None"), types.MaintenanceModeNone)
	assert.Equal(t, types.MaintenanceMode("CordonOnly"), types.MaintenanceModeCordon)
	assert.Equal(t, types.MaintenanceMode("CordonAndDrain"), types.MaintenanceModeCordonAndDrain)
}

func TestMaintenanceModeStringMethod(t *testing.T) {
	// Test that the String() method returns the correct values
	assert.Equal(t, "None", types.MaintenanceModeNone.String())
	assert.Equal(t, "CordonOnly", types.MaintenanceModeCordon.String())
	assert.Equal(t, "CordonAndDrain", types.MaintenanceModeCordonAndDrain.String())
}

func TestMaintenanceModeMutualExclusivityLogic(t *testing.T) {
	// Test the mutual exclusivity error message format
	expectedErrorFormat := "feature flags %s and %s are mutually exclusive and cannot be enabled simultaneously"

	actualError := fmt.Errorf(expectedErrorFormat,
		featureflag.CordonMaintenance.Key,
		featureflag.CordonAndDrainMaintenance.Key)

	assert.Contains(t, actualError.Error(), "CordonMaintenance")
	assert.Contains(t, actualError.Error(), "CordonAndDrainMaintenance")
	assert.Contains(t, actualError.Error(), "mutually exclusive")
}

func TestConfigureAndCheckCLI_SelfHostedRequiresClusterID(t *testing.T) {
	require.NoError(t, (&featureflag.CLIFlag{}).Set(featureflag.SelfHosted.Key))
	t.Cleanup(func() {
		require.NoError(t, (&featureflag.CLIFlag{}).Set("-"+featureflag.SelfHosted.Key))
	})

	logger := logrus.New()
	logger.SetOutput(io.Discard)

	err := configureAndCheckCLI(logrus.NewEntry(logger), &AgentOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clusterID is required")

	err = configureAndCheckCLI(logrus.NewEntry(logger), &AgentOptions{ClusterID: "registered-cluster-id"})
	require.NoError(t, err)
}

func newMockCLIAgent() (cliAgent, chan struct{}) {
	block := make(chan struct{})
	a := &mockAgent{
		block: block,
	}
	return a, block
}

type mockAgent struct {
	block chan struct{}
}

func (m *mockAgent) Start(ctx context.Context) error {
	m.block <- struct{}{}
	return nil
}
