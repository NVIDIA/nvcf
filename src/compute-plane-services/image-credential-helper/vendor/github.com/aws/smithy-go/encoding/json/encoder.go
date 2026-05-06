// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package json

import (
	"bytes"
)

// Encoder is JSON encoder that supports construction of JSON values
// using methods.
type Encoder struct {
	w *bytes.Buffer
	Value
}

// NewEncoder returns a new JSON encoder
func NewEncoder() *Encoder {
	writer := bytes.NewBuffer(nil)
	scratch := make([]byte, 64)

	return &Encoder{w: writer, Value: newValue(writer, &scratch)}
}

// String returns the String output of the JSON encoder
func (e Encoder) String() string {
	return e.w.String()
}

// Bytes returns the []byte slice of the JSON encoder
func (e Encoder) Bytes() []byte {
	return e.w.Bytes()
}
