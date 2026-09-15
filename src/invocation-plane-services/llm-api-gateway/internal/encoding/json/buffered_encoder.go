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

package json

import (
	"bytes"
	"io"

	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/pool"
	"github.com/NVIDIA/nvcf/src/invocation-plane-services/llm-gateway/internal/ptr"
)

// A Buffer is a byte buffer. It may be (but is not guaranteed to always be)
// a wrapper around [bytes.Buffer].
type Buffer interface {
	io.Reader
	io.WriterTo

	// Len returns the length of the buffer in bytes.
	Len() int

	// Cap returns the current capacity of the buffer in bytes.
	Cap() int

	// Bytes returns the buffer's underlying data.
	Bytes() []byte

	// String allocates a new string containing the buffer's underlying data.
	String() string

	// Reset resets the buffer for future use.
	Reset()
}

// A BufferedEncoder is an [Encoder] with a reusable internal buffer. It is
// designed for pooling in order to amortize epehemeral storage costs.
type BufferedEncoder struct {
	enc Encoder
	buf bytes.Buffer
}

// NewBufferedEncoder returns a new [BufferedEncoder] with an initial buffer
// capacity of 0. It is sugar for NewBufferedEncoderWithCapacity(0).
func NewBufferedEncoder() *BufferedEncoder {
	return NewBufferedEncoderWithCapacity(0)
}

// NewBufferedEncoderWithCapacity returns a new [BufferedEncoder] with the
// given initial buffer capacity.
func NewBufferedEncoderWithCapacity(capacity int) *BufferedEncoder {
	// n.b. Intentionally initialized as a pointer for addressability
	enc := new(BufferedEncoder)
	if capacity > 0 {
		enc.buf.Grow(capacity)
	}
	enc.enc = ptr.Deref(NewEncoder(&enc.buf))
	return enc
}

// Buffer returns the [Buffer] holding the underlying encoded data.
func (e *BufferedEncoder) Buffer() Buffer {
	return &e.buf
}

// Encode is a wrapper around [Encoder.Encode]. Note that, unlike the JSON
// produced by [Marshal], Encode will append a newline to the end encoded data.
func (e *BufferedEncoder) Encode(v any) error {
	return e.enc.Encode(v)
}

// SetEscapeHTML is a wrapper around [Encoder.SetEscapeHTML].
func (e *BufferedEncoder) SetEscapeHTML(on bool) {
	e.enc.SetEscapeHTML(on)
}

// SetIndent is a wrapper around [Encoder.SetIndent].
func (e *BufferedEncoder) SetIndent(prefix string, indent string) {
	e.enc.SetIndent(prefix, indent)
}

// String is syntactic sugar for calling [BufferedEncoder.Buffer().String].
func (e *BufferedEncoder) String() string {
	if x := e.Bytes(); len(x) > 0 {
		return string(x)
	}
	return ""
}

// TrimmedString calls [BufferedEncoder.String] and trims whitespace from the
// result.
func (e *BufferedEncoder) TrimmedString() string {
	if x := e.TrimmedBytes(); len(x) > 0 {
		return string(x)
	}
	return ""
}

// Bytes is syntactic sugar for calling [BufferedEncoder.Buffer().Bytes].
func (e *BufferedEncoder) Bytes() []byte {
	return e.buf.Bytes()
}

// TrimmedBytes calls [BufferedEncoder.Bytes] and trims whitespace from the
// result.
func (e *BufferedEncoder) TrimmedBytes() []byte {
	if e.buf.Len() == 0 {
		return nil
	}

	x := e.Bytes()
	if end := len(x) - 1; x[end] == '\n' {
		x = x[:end]
	}

	return x
}

// BufferedEncoderPool is a [pool.Pool] specialized for [BufferedEncoder].
type BufferedEncoderPool = pool.Pool[*BufferedEncoder]

// NewBufferedEncoderPool creates a new [BufferedEncoderPool] that produces
// [BufferedEncoder]s with the given initial buffer capacity.
func NewBufferedEncoderPool(bufferCapacity int) *BufferedEncoderPool {
	return pool.NewWithReleaser(
		func() *BufferedEncoder {
			return NewBufferedEncoderWithCapacity(bufferCapacity)
		},
		func(x *BufferedEncoder) {
			x.Buffer().Reset()
		},
	)
}
