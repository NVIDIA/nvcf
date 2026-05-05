// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nvcaconfig

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_splitByCamelCase(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single_lower", "a", []string{"a"}},
		{"single_upper", "A", []string{"A"}},
		{"all_lower", "abc", []string{"abc"}},
		{"all_upper", "ABC", []string{"ABC"}},
		{"camel_case", "camelCase", []string{"camel", "Case"}},
		{"pascal_case", "PascalCase", []string{"Pascal", "Case"}},
		{"acronym_start", "HTTPServer", []string{"HTTP", "Server"}},
		{"acronym_end", "serverHTTP", []string{"server", "HTTP"}},
		{"with_digits", "Test123Value", []string{"Test", "123", "Value"}},
		{"digits_only", "12345", []string{"12345"}},
		{"mixed_all", "getHTTP2Response", []string{"get", "HTTP", "2", "Response"}},
		{"invalid_utf8", string([]byte{0xff, 0xfe}), []string{string([]byte{0xff, 0xfe})}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitByCamelCase(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_joinByCamelCase(t *testing.T) {
	for _, c := range [][2]string{
		{"", ""},
		{"a", "a"},
		{"A", "A"},
		{"aa", "aa"},
		{"Aa", "Aa"},
		{"AA", "AA"},
		{"Aaa", "Aaa"},
		{"AaaA", "Aaa_A"},
		{"AAa", "A_Aa"},
		{"AAAa", "AA_Aa"},
	} {
		t.Run(fmt.Sprintf("%s_%s", c[0], c[1]), func(t *testing.T) {
			assert.Equal(t, c[1], joinByCamelCase(c[0], "_"))
		})
	}
}

func Test_nameToCamelCase(t *testing.T) {
	for _, c := range [][2]string{
		{"", ""},
		{"a", "a"},
		{"A", "a"},
		{"aa", "aa"},
		{"Aa", "aa"},
		{"AA", "aa"},
		{"Aaa", "aaa"},
		{"AaaA", "aaaA"},
		{"AAa", "aAa"},
		{"AAAa", "aaAa"},
	} {
		t.Run(fmt.Sprintf("%s_%s", c[0], c[1]), func(t *testing.T) {
			assert.Equal(t, c[1], nameToCamelCase(c[0]))
		})
	}
}

func Test_joinPrefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		s      string
		sep    string
		squash bool
		want   string
	}{
		{"empty_prefix", "", "field", ".", false, "field"},
		{"with_prefix", "parent", "field", ".", false, "parent.field"},
		{"squash_returns_prefix", "parent", "field", ".", true, "parent"},
		{"underscore_sep", "ENV", "VAR", "_", false, "ENV_VAR"},
		{"squash_ignores_field", "parent.nested", "ignored", ".", true, "parent.nested"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinPrefix(tt.prefix, tt.s, tt.sep, tt.squash)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_recursiveAutobind_errors(t *testing.T) {
	t.Run("nil_config", func(t *testing.T) {
		err := recursiveAutobind(nil, "PREFIX_", "", "", "", func(_, _, _ string) error {
			return nil
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config must not be nil")
	})

	t.Run("non_pointer", func(t *testing.T) {
		err := recursiveAutobind(Config{}, "PREFIX_", "", "", "", func(_, _, _ string) error {
			return nil
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config must not be nil")
	})

	t.Run("non_struct_pointer", func(t *testing.T) {
		s := "not a struct"
		err := recursiveAutobind(&s, "PREFIX_", "", "", "", func(_, _, _ string) error {
			return nil
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config must be type struct")
	})

	t.Run("bind_error_propagates", func(t *testing.T) {
		type simpleConfig struct {
			Field string
		}
		expectedErr := errors.New("bind failed")
		err := recursiveAutobind(&simpleConfig{}, "PREFIX_", "", "", "", func(_, _, _ string) error {
			return expectedErr
		})
		assert.ErrorIs(t, err, expectedErr)
	})
}

func Test_AutobindAll(t *testing.T) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	err := AutobindAll(v)
	require.NoError(t, err)

	// Verify some env bindings work
	t.Setenv("NVCA_AGENT_LOG_LEVEL", "debug")
	assert.Equal(t, "debug", v.GetString("agent.loglevel"))

	// Verify alias works (camelCase -> lowercase)
	assert.Equal(t, "debug", v.GetString("agent.logLevel"))
}

func Test_AutobindEnvs(t *testing.T) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	err := AutobindEnvs(v)
	require.NoError(t, err)

	t.Setenv("NVCA_CLUSTER_NAME", "test-cluster")
	assert.Equal(t, "test-cluster", v.GetString("cluster.name"))
}

func Test_AutobindAliases(t *testing.T) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	err := AutobindAliases(v)
	require.NoError(t, err)

	// Set value via lowercase key
	v.Set("agent.loglevel", "warn")

	// Should be accessible via camelCase alias
	assert.Equal(t, "warn", v.GetString("agent.logLevel"))
}
