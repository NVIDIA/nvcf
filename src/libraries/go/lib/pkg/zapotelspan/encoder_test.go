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

package zapotelspan

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap/zapcore"
)

func findAttr(attrs []attribute.KeyValue, key string) (attribute.KeyValue, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a, true
		}
	}
	return attribute.KeyValue{}, false
}

func TestOtelAttrsEncoder_primitives(t *testing.T) {
	e := NewOtelAttrsEncoder()
	e.AddString("s", "v")
	e.AddBool("b", true)
	e.AddInt("i", -3)
	e.AddInt64("i64", math.MinInt64)
	e.AddInt32("i32", 7)
	e.AddInt16("i16", 8)
	e.AddInt8("i8", 9)
	e.AddFloat64("f64", 1.25)
	e.AddFloat32("f32", 2.5)
	e.AddUint8("u8", 255)
	e.AddUint16("u16", 65535)
	e.AddUint32("u32", 1<<31)
	e.AddUint64("u64_small", 42)
	e.AddUint64("u64_big", uint64(math.MaxInt64)+1)
	e.AddUint("u_fit", 100)
	e.AddUintptr("up", 0x1000)
	e.AddComplex64("c64", 1+2i)
	e.AddComplex128("c128", 3+4i)
	e.AddDuration("d", time.Minute)
	e.AddTime("tm", time.Date(2020, 1, 2, 3, 4, 5, 6, time.UTC))
	e.AddBinary("bin", []byte{0, 1})
	e.AddByteString("bs", []byte("x"))
	e.AddReflected("r", map[string]int{"a": 1})
	e.OpenNamespace("ns")

	attrs := e.GetAttrs()
	a, ok := findAttr(attrs, "u64_big")
	require.True(t, ok)
	require.Equal(t, attribute.STRING, a.Value.Type())
	require.Equal(t, "9223372036854775808", a.Value.AsString())

	a, ok = findAttr(attrs, "u64_small")
	require.True(t, ok)
	require.Equal(t, attribute.INT64, a.Value.Type())
	require.Equal(t, int64(42), a.Value.AsInt64())

	a, ok = findAttr(attrs, "i64")
	require.True(t, ok)
	require.Equal(t, int64(math.MinInt64), a.Value.AsInt64())
}

func TestOtelAttrsEncoder_AddArray(t *testing.T) {
	e := NewOtelAttrsEncoder()
	require.NoError(t, e.AddArray("a", zapcore.ArrayMarshalerFunc(func(enc zapcore.ArrayEncoder) error {
		enc.AppendString("x")
		return nil
	})))
	attrs := e.GetAttrs()
	a, ok := findAttr(attrs, "a")
	require.True(t, ok)
	require.NotEmpty(t, a.Value.AsString())
}

func TestOtelAttrsEncoder_AddObject(t *testing.T) {
	e := NewOtelAttrsEncoder()
	require.NoError(t, e.AddObject("o", zapcore.ObjectMarshalerFunc(func(enc zapcore.ObjectEncoder) error {
		enc.AddString("k", "v")
		return nil
	})))
	attrs := e.GetAttrs()
	a, ok := findAttr(attrs, "o")
	require.True(t, ok)
	require.Contains(t, a.Value.AsString(), "k")
}
