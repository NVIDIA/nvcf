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

package tokenizers

import (
	"strings"
	"unsafe"

	"github.com/nvidia-lpu/harmony"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

type HarmonyTokenizer struct {
	tokenizer harmony.Encoding
}

var _ Tokenizer = (*HarmonyTokenizer)(nil)

func NewHarmonyTokenizer() (*HarmonyTokenizer, error) {
	enc, err := harmony.NewEncoding()
	if err != nil {
		return nil, err
	}
	return &HarmonyTokenizer{tokenizer: enc}, nil
}

func (t *HarmonyTokenizer) Encode(text string) ([]uint32, error) {
	ids, err := t.tokenizer.EncodeWithSpecialTokens(text, true, nil)
	if err != nil {
		return nil, err
	}
	return unsafeSliceConvert[harmony.Rank, uint32](ids), nil
}

func (t *HarmonyTokenizer) Decode(ids []uint32, _ bool) (string, error) {
	ranks := unsafeSliceConvert[uint32, harmony.Rank](ids)
	str, err := t.tokenizer.Decode(ranks)
	switch {
	case err == nil:
		return str, nil
	case strings.Contains(err.Error(), "Invalid utf-8 sequence"):
		// UTF-8 characters may be split between multiple tokens
		// The other tokenizers return a replacement character in these cases,
		// So we return the same for compatibility.
		return "", ErrIncompleteUTF8Character
	default:
		return "", err
	}
}

func unsafeSliceConvert[T1, T2 any](src []T1) []T2 {
	// Ensure types have same size
	if unsafe.Sizeof(ptr.Deref(new(T1))) != unsafe.Sizeof(ptr.Deref(new(T2))) {
		panic("cannot convert: different element sizes")
	}

	// Reinterpret slice header
	return unsafe.Slice((*T2)(unsafe.Pointer(unsafe.SliceData(src))), len(src))
}
