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
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cli "github.com/urfave/cli/v2"
)

func TestNewCobraCommand(t *testing.T) {
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
		cmd := newCobraCommand(
			newAgentFunc(a),
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
		assert.NoError(t, err)
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
  helmReValServiceURL: http://reval.localhost:18080
  NATSURL: nats://nats.localhost:14222
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
		assert.Equal(t, "http://reval.localhost:18080", gotOpts.HelmReValServiceURL)
		assert.Equal(t, "nats://nats.localhost:14222", gotOpts.NATSURL)
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
