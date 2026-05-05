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

package tools

import (
	"math/rand/v2"
	"strings"

	"go.jetify.com/typeid"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
)

const (
	_callIDLength = 9
)

// CallIDPrefix is the prefix specialization for [typeid.TypeID] to produce a
// [CallID].
type CallIDPrefix struct{}

// Prefix returns the static prefix to be used for all [CallID]s.
func (CallIDPrefix) Prefix() string {
	return "call"
}

// A CallID is a semi-unique identifier for calls.
type CallID struct {
	typeid.TypeID[CallIDPrefix]
}

func newCallIDBase() CallID {
	return must.Get(typeid.New[CallID]())
}

func NewCallID() string {
	return newCallIDBase().String()
}

// NewShortCallID returns a short, random [CallID] suffix as a string.
func NewShortCallID() string {
	id := newCallIDBase().String()
	return id[len(id)-_callIDLength:]
}

func NewMistralToolCallID() string {
	// n.b. The mistral-large template will raise an error if the generated ID
	//      does not follow these constraints.
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	var b strings.Builder
	b.Grow(_callIDLength)

	for range _callIDLength {
		b.WriteByte(charset[rand.Int64()%_callIDLength]) //nolint:gosec
	}

	return b.String()
}
