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
	"fmt"
	"math"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap/zapcore"
)

type otelAttrsEncoder struct {
	attrs []attribute.KeyValue
}

// NewOtelAttrsEncoder creates a new otelAttrsEncoder.
func NewOtelAttrsEncoder() *otelAttrsEncoder {
	return &otelAttrsEncoder{
		attrs: make([]attribute.KeyValue, 0),
	}
}

// GetAttrs returns the accumulated attributes.
func (e *otelAttrsEncoder) GetAttrs() []attribute.KeyValue {
	return e.attrs
}

// AddArray implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddArray(key string, marshaler zapcore.ArrayMarshaler) error {
	e.attrs = append(e.attrs, attribute.String(key, fmt.Sprintf("%v", marshaler)))
	return nil
}

// AddObject implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddObject(key string, marshaler zapcore.ObjectMarshaler) error {
	enc := zapcore.NewMapObjectEncoder()
	err := marshaler.MarshalLogObject(enc)
	if err != nil {
		return err
	}
	e.attrs = append(e.attrs, attribute.String(key, fmt.Sprintf("%v", enc)))
	return nil
}

// AddBinary implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddBinary(key string, value []byte) {
	e.attrs = append(e.attrs, attribute.String(key, string(value)))
}

// AddByteString implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddByteString(key string, value []byte) {
	e.attrs = append(e.attrs, attribute.String(key, string(value)))
}

// AddBool implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddBool(key string, value bool) {
	e.attrs = append(e.attrs, attribute.Bool(key, value))
}

// AddComplex128 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddComplex128(key string, value complex128) {
	e.attrs = append(e.attrs, attribute.String(key, fmt.Sprintf("%v", value)))
}

// AddComplex64 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddComplex64(key string, value complex64) {
	e.attrs = append(e.attrs, attribute.String(key, fmt.Sprintf("%v", value)))
}

// AddDuration implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddDuration(key string, value time.Duration) {
	e.attrs = append(e.attrs, attribute.String(key, value.String()))
}

// AddFloat64 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddFloat64(key string, value float64) {
	e.attrs = append(e.attrs, attribute.Float64(key, value))
}

// AddFloat32 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddFloat32(key string, value float32) {
	e.attrs = append(e.attrs, attribute.Float64(key, float64(value)))
}

// AddInt implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddInt(key string, value int) {
	e.attrs = append(e.attrs, attribute.Int(key, value))
}

// AddInt64 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddInt64(key string, value int64) {
	e.attrs = append(e.attrs, attribute.Int64(key, value))
}

// AddInt32 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddInt32(key string, value int32) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddInt16 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddInt16(key string, value int16) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddInt8 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddInt8(key string, value int8) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddString implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddString(key string, value string) {
	e.attrs = append(e.attrs, attribute.String(key, value))
}

// AddTime implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddTime(key string, value time.Time) {
	e.attrs = append(e.attrs, attribute.String(key, value.Format(time.RFC3339Nano)))
}

// uint64AsInt64Attr records u as Int64 when it fits in int64; otherwise as a decimal string.
// Values are routed through ParseInt to avoid gosec G115 on direct unsigned→signed casts.
func uint64AsInt64Attr(key string, u uint64) attribute.KeyValue {
	if u > uint64(math.MaxInt64) {
		return attribute.String(key, strconv.FormatUint(u, 10))
	}
	i, err := strconv.ParseInt(strconv.FormatUint(u, 10), 10, 64)
	if err != nil {
		return attribute.String(key, strconv.FormatUint(u, 10))
	}
	return attribute.Int64(key, i)
}

// AddUint implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUint(key string, value uint) {
	e.attrs = append(e.attrs, uint64AsInt64Attr(key, uint64(value)))
}

// AddUint64 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUint64(key string, value uint64) {
	e.attrs = append(e.attrs, uint64AsInt64Attr(key, value))
}

// AddUint32 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUint32(key string, value uint32) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddUint16 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUint16(key string, value uint16) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddUint8 implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUint8(key string, value uint8) {
	e.attrs = append(e.attrs, attribute.Int64(key, int64(value)))
}

// AddUintptr implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddUintptr(key string, value uintptr) {
	e.attrs = append(e.attrs, uint64AsInt64Attr(key, uint64(value)))
}

// AddReflected implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) AddReflected(key string, value interface{}) error {
	e.attrs = append(e.attrs, attribute.String(key, fmt.Sprintf("%v", value)))
	return nil
}

// OpenNamespace implements zapcore.ObjectEncoder.
func (e *otelAttrsEncoder) OpenNamespace(key string) {
	_ = key
}

var _ zapcore.ObjectEncoder = (*otelAttrsEncoder)(nil)
