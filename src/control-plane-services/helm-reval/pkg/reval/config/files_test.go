// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/reval/config"
)

func nopLogger() *zap.Logger {
	l, _ := zap.NewDevelopment()
	return l
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cfg-*.yaml")
	require.NoError(t, err)
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

func TestParseConfigFiles_EmptyList(t *testing.T) {
	v := viper.New()
	err := config.ParseConfigFiles([]string{}, v, nopLogger())
	require.NoError(t, err)
}

func TestParseConfigFiles_WhitespaceOnlyEntries(t *testing.T) {
	v := viper.New()
	err := config.ParseConfigFiles([]string{"   ", "\t", ""}, v, nopLogger())
	require.NoError(t, err)
}

func TestParseConfigFiles_SingleFile(t *testing.T) {
	path := writeYAML(t, "http:\n  api-port: 9090\n")
	v := viper.New()
	require.NoError(t, config.ParseConfigFiles([]string{path}, v, nopLogger()))
	assert.Equal(t, 9090, v.GetInt("http.api-port"))
}

func TestParseConfigFiles_MergesMultipleFiles(t *testing.T) {
	path1 := writeYAML(t, "http:\n  api-port: 9090\n")
	path2 := writeYAML(t, "http:\n  metrics-port: 9091\n")
	v := viper.New()
	require.NoError(t, config.ParseConfigFiles([]string{path1, path2}, v, nopLogger()))
	assert.Equal(t, 9090, v.GetInt("http.api-port"))
	assert.Equal(t, 9091, v.GetInt("http.metrics-port"))
}

func TestParseConfigFiles_LaterFileOverridesEarlier(t *testing.T) {
	path1 := writeYAML(t, "http:\n  api-port: 8080\n")
	path2 := writeYAML(t, "http:\n  api-port: 9999\n")
	v := viper.New()
	require.NoError(t, config.ParseConfigFiles([]string{path1, path2}, v, nopLogger()))
	assert.Equal(t, 9999, v.GetInt("http.api-port"))
}

func TestParseConfigFiles_MissingFile_ReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nonexistent.yaml")
	v := viper.New()
	err := config.ParseConfigFiles([]string{missing}, v, nopLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to merge config file")
}

func TestParseConfigFiles_InvalidYAML_ReturnsError(t *testing.T) {
	path := writeYAML(t, "this: is: invalid: yaml:\n  - [broken")
	v := viper.New()
	err := config.ParseConfigFiles([]string{path}, v, nopLogger())
	require.Error(t, err)
}

func TestParseConfigFiles_SkipsEmptyAfterTrim(t *testing.T) {
	path := writeYAML(t, "http:\n  api-port: 7777\n")
	v := viper.New()
	// Mix empty/whitespace with a valid path
	require.NoError(t, config.ParseConfigFiles([]string{"", "  ", path}, v, nopLogger()))
	assert.Equal(t, 7777, v.GetInt("http.api-port"))
}
