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
	"encoding/json"
	"io"
)

// n.b. This file contains re-exports of upstream (stdlib encoding/json) types
//      and functions so that this package can be imported as a drop-in
//      replacement of encoding/json while also utilizing internal helpers like
//      [BufferedEncoder] without needing to import multiple related packages.

// Marshaler is a re-export of [json.Marshaler].
type Marshaler = json.Marshaler

// Marshal is a wrapper around [json.Marshal].
func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// MarshalIndent is a wrapper around [json.MarshalIndent].
func MarshalIndent(v any, prefix string, indent string) ([]byte, error) {
	return json.MarshalIndent(v, prefix, indent)
}

// Unmarshaler is a re-export of [json.Unmarshaler].
type Unmarshaler = json.Unmarshaler

// Unmarshal is a wrapper around [json.Unmarshal].
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// Encoder is a re-export of [json.Encoder].
type Encoder = json.Encoder

// NewEncoder is a wrapper around [json.NewEncoder].
func NewEncoder(w io.Writer) *Encoder {
	return json.NewEncoder(w)
}

// Decoder is a re-export of [json.Decoder].
type Decoder = json.Decoder

// NewDecoder is a wrapper around [json.NewDecoder].
func NewDecoder(r io.Reader) *Decoder {
	return json.NewDecoder(r)
}

// RawMessage is a re-export of [json.RawMessage].
type RawMessage = json.RawMessage

// SyntaxError is a re-export of [json.SyntaxError].
type SyntaxError = json.SyntaxError

// UnmarshalTypeError is a re-export of [json.UnmarshalTypeError].
type UnmarshalTypeError = json.UnmarshalTypeError
