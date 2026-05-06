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

// Package token provides tokenization-adjacent helpers.
package token

import (
	"io"
	"slices"
	"strconv"

	"github.com/NVIDIA/nvcf/llm-api-gateway/models"
)

var (
	_ Content = ContentTokens(nil)
	_ Content = (*ContentImage)(nil)
	_ Content = ContentText("")
)

type Content interface {
	ContentType() models.ContentPartType
	WriteTo(dst io.StringWriter) error
}

type Tokens []Content

func (t Tokens) Raw() []uint32 {
	var dst []uint32
	for _, x := range t {
		if tok, ok := x.(ContentTokens); ok {
			dst = slices.Grow(dst, len(tok))
			dst = append(dst, tok...)
		}
	}

	if len(dst) == 0 {
		return nil
	}

	return dst
}

type ContentTokens []uint32

func (ContentTokens) ContentType() models.ContentPartType {
	return "text-tokens"
}

func (t ContentTokens) WriteTo(dst io.StringWriter) error {
	for i, tok := range t {
		if i > 0 {
			if _, err := dst.WriteString(" "); err != nil {
				return err
			}
		}
		if _, err := dst.WriteString(strconv.Itoa(int(tok))); err != nil {
			return err
		}
	}

	return nil
}

type ContentImage struct {
	*models.ContentPartImageURL
}

func NewContentImage(u *models.ContentPartImageURL) ContentImage {
	return ContentImage{
		ContentPartImageURL: u,
	}
}

func (ContentImage) ContentType() models.ContentPartType {
	return "image"
}

func (t ContentImage) WriteTo(dst io.StringWriter) error {
	_, err := dst.WriteString("<|image|>")
	return err
}

// TODO(mway): Remove once all models are tokenized in Orion
type ContentText string

func (ContentText) ContentType() models.ContentPartType {
	return "text"
}

func (t ContentText) WriteTo(dst io.StringWriter) error {
	_, err := dst.WriteString(string(t))
	return err
}
