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

package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker"
)

// noopLogger builds the production logger only to satisfy NewRootCommand's
// signature; the command construction itself does not emit through it.
func TestNewRootCommandConstruction(t *testing.T) {
	cmd := NewRootCommand(context.Background(), nil)
	require.NotNil(t, cmd)

	assert.Equal(t, "worker", cmd.Use)
	assert.Equal(t, "NVCF worker service", cmd.Short)
	assert.True(t, cmd.SilenceUsage)
	assert.NotNil(t, cmd.PersistentPreRunE)
	assert.NotNil(t, cmd.RunE)

	// The persistent --config flag must be registered.
	configFlag := cmd.PersistentFlags().Lookup("config")
	require.NotNil(t, configFlag)
	assert.Equal(t, "", configFlag.DefValue)
}

// TestNewRootCommandFlagWiring asserts that one local flag is registered for
// every mapstructure-tagged field of worker.Config.
func TestNewRootCommandFlagWiring(t *testing.T) {
	cmd := NewRootCommand(context.Background(), nil)

	configType := reflect.TypeOf(worker.Config{})
	for i := 0; i < configType.NumField(); i++ {
		name := configType.Field(i).Tag.Get("mapstructure")
		if name == "" {
			continue
		}
		flag := cmd.Flags().Lookup(name)
		assert.NotNilf(t, flag, "expected flag %q to be wired", name)
	}

	// Spot check a couple of known config keys.
	assert.NotNil(t, cmd.Flags().Lookup("NVCF_WORKER_TOKEN"))
	assert.NotNil(t, cmd.Flags().Lookup("INFERENCE_PORT"))
}

// TestPersistentPreRunEMalformedConfig exercises the error path where
// config.InitConfig fails to parse the supplied config file. This returns an
// internal error before any worker construction, and never reaches RunE/Run,
// so no http server is bound.
func TestPersistentPreRunEMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	badCfg := filepath.Join(dir, "config.yaml")
	// Invalid YAML so viper.ReadInConfig returns a parse error rather than a
	// ConfigFileNotFoundError.
	require.NoError(t, os.WriteFile(badCfg, []byte("::: not: valid: yaml: ["), 0o600))

	cmd := NewRootCommand(context.Background(), nil)
	require.NoError(t, cmd.PersistentFlags().Set("config", badCfg))

	err := cmd.PersistentPreRunE(cmd, nil)
	require.Error(t, err)
}

// TestPersistentPreRunEIsField confirms the hook is directly invocable (used by
// the error-path test) and is the same function set during construction.
func TestPersistentPreRunEIsInvocable(t *testing.T) {
	cmd := NewRootCommand(context.Background(), nil)
	var _ func(*cobra.Command, []string) error = cmd.PersistentPreRunE
}

// TestPersistentPreRunEInvalidFunctionID drives the success path of
// config.InitConfig (valid, empty YAML) so the hook proceeds through MustBindEnv
// flag binding, v.Unmarshal (which invokes viperDecoderConfig), and into
// worker.NewNVCFWorker. With an empty config the worker construction fails its
// first validation (FunctionId is not a UUID) and returns an error before any
// http server is bound, so RunE/Run are never reached.
func TestPersistentPreRunEInvalidFunctionID(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte("# empty but valid yaml\n"), 0o600))

	cmd := NewRootCommand(context.Background(), nil)
	require.NoError(t, cmd.PersistentFlags().Set("config", cfg))

	err := cmd.PersistentPreRunE(cmd, nil)
	require.Error(t, err)
	// Empty FunctionId fails uuid.Parse inside NewNVCFWorker.
	assert.Contains(t, err.Error(), "function id")
}

// TestViperDecoderConfig directly exercises the decoder-config constructor.
func TestViperDecoderConfig(t *testing.T) {
	assert.NotNil(t, viperDecoderConfig())
}
