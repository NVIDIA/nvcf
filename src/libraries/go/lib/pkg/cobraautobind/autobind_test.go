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

package cobraautobind

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

type testCfg struct {
	Name string `mapstructure:"name" default:"x" usage:"name usage"`
	N    struct {
		Port uint16 `mapstructure:"port" default:"8080" usage:"port"`
	}
}

func TestAutobindFlagsFromStruct(t *testing.T) {
	cmd := &cobra.Command{Use: "t"}
	v := viper.New()
	require.NoError(t, AutobindFlagsFromStruct(cmd, v, &testCfg{}))
	require.NoError(t, cmd.ParseFlags([]string{"--name", "hello", "--n.port", "9090"}))
	require.Equal(t, "hello", v.GetString("name"))
	require.Equal(t, uint16(9090), uint16(v.GetUint("n.port")))
}

type wideCfg struct {
	S     string   `mapstructure:"s" default:"def" usage:"s usage"`
	I     int      `mapstructure:"i" default:"-2"`
	I32   int32    `mapstructure:"i32" default:"32"`
	I64   int64    `mapstructure:"i64" default:"640"`
	U16   uint16   `mapstructure:"u16" default:"100"`
	B     bool     `mapstructure:"b" default:"true"`
	F64   float64  `mapstructure:"f64" default:"3.5"`
	F32   float32  `mapstructure:"f32" default:"1.25"`
	Tags  []string `mapstructure:"tags" usage:"tags"`
	Alias string   `flag:"alias-flag" mapstructure:"alias" default:"af"`
	Nest  struct {
		Inner string `mapstructure:"inner" default:"in"`
	}
	ignored string `mapstructure:"-"`
}

func TestAutobindFlagsFromStruct_allKinds(t *testing.T) {
	cmd := &cobra.Command{Use: "t"}
	v := viper.New()
	require.NoError(t, AutobindFlagsFromStruct(cmd, v, &wideCfg{}))
	require.NoError(t, cmd.ParseFlags([]string{
		"--s", "hi",
		"--i", "7",
		"--i32", "9",
		"--i64", "99",
		"--u16", "200",
		"--b=false",
		"--f64", "2.5",
		"--f32", "4",
		"--tags", "a,b",
		"--alias-flag", "xx",
		"--nest.inner", "deep",
	}))
	require.Equal(t, "hi", v.GetString("s"))
	require.Equal(t, 7, v.GetInt("i"))
	require.Equal(t, int32(9), v.GetInt32("i32"))
	require.Equal(t, int64(99), v.GetInt64("i64"))
	require.Equal(t, uint16(200), uint16(v.GetUint("u16")))
	require.False(t, v.GetBool("b"))
	require.InDelta(t, 2.5, v.GetFloat64("f64"), 0.001)
	require.InDelta(t, float32(4), float32(v.GetFloat64("f32")), 0.001)
	require.Equal(t, []string{"a", "b"}, v.GetStringSlice("tags"))
	require.Equal(t, "xx", v.GetString("alias-flag"))
	require.Equal(t, "deep", v.GetString("nest.inner"))
}

func TestAutobindFlagsFromStruct_errors(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		require.ErrorIs(t, AutobindFlagsFromStruct(&cobra.Command{}, viper.New(), nil), ErrConfigMustBeNonNil)
	})
	t.Run("nil typed", func(t *testing.T) {
		require.ErrorIs(t, AutobindFlagsFromStruct(&cobra.Command{}, viper.New(), (*testCfg)(nil)), ErrConfigMustBeNonNil)
	})
	t.Run("not struct", func(t *testing.T) {
		n := 3
		require.ErrorIs(t, AutobindFlagsFromStruct(&cobra.Command{}, viper.New(), &n), ErrConfigMustBeStruct)
	})
	t.Run("bad default", func(t *testing.T) {
		type badInt struct {
			X int `mapstructure:"x" default:"nope"`
		}
		require.Error(t, AutobindFlagsFromStruct(&cobra.Command{Use: "t"}, viper.New(), &badInt{}))
	})
	t.Run("unsupported field", func(t *testing.T) {
		type badChan struct {
			C chan int `mapstructure:"c"`
		}
		require.ErrorAs(t, AutobindFlagsFromStruct(&cobra.Command{Use: "t"}, viper.New(), &badChan{}), new(*UnsupportedFieldTypeError))
	})
	t.Run("non-string slice skipped", func(t *testing.T) {
		type nonStringSlice struct {
			N []int `mapstructure:"n"`
		}
		require.NoError(t, AutobindFlagsFromStruct(&cobra.Command{Use: "t"}, viper.New(), &nonStringSlice{}))
	})
}
