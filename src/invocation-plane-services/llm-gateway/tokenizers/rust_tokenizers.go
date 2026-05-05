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
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/daulet/tokenizers"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/ptr"
)

type rustTokenizer struct {
	*tokenizers.Tokenizer

	bos *uint32
}

var _ Tokenizer = (*rustTokenizer)(nil)

func (t *rustTokenizer) Encode(text string) ([]uint32, error) {
	ids, _ := t.Tokenizer.Encode(text, true)
	if t.bos != nil {
		out := make([]uint32, 1, len(ids)+1)
		out[0] = ptr.Deref(t.bos)
		out = append(out, ids...)
		ids = out
	}
	return ids, nil
}

func (t *rustTokenizer) Decode(ids []uint32, skipSpecial bool) (string, error) {
	str := t.Tokenizer.Decode(ids, skipSpecial)
	if strings.Contains(str, "�") {
		return "", ErrIncompleteUTF8Character
	}
	return str, nil
}

func NewHFTokenizer(path string) (Tokenizer, error) {
	hf, err := tokenizers.FromFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to load HF tokenizer, path: %s: %w", path, err)
	}
	return &rustTokenizer{Tokenizer: hf}, nil
}

func loadTiktokenPattern(patternPath string) (string, error) {
	pattern, err := os.ReadFile(patternPath)
	if err != nil {
		return "", fmt.Errorf("failed to read pattern.txt: %w", err)
	}
	var (
		parts []string
		sc    = bufio.NewScanner(strings.NewReader(string(pattern)))
	)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts = append(parts, line)
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	full := strings.Join(parts, "|")
	return full, err
}

func NewTikTokenTokenizer(path string) (Tokenizer, error) {
	var (
		tiktokenPath        = filepath.Join(path, "tiktoken.model")
		tokenizerConfigPath = filepath.Join(path, "tokenizer_config.json")
		patternPath         = filepath.Join(path, "pattern.txt")
	)
	pattern, err := loadTiktokenPattern(patternPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load tiktoken pattern, path: %s: %w", patternPath, err)
	}

	bosTokenStr := getAddBosTokenString(tokenizerConfigPath)

	t, err := tokenizers.FromTiktoken(tiktokenPath, tokenizerConfigPath, pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to load tiktoken tokenizer, path: %s: %w", path, err)
	}
	var bosTokenID *uint32
	if bosTokenStr != nil {
		ids, _ := t.Encode(ptr.Deref(bosTokenStr), true)
		if len(ids) != 1 {
			return nil, errors.New("failed to encode bos token")
		}
		bosTokenID = &ids[0]
	}
	return &rustTokenizer{
		Tokenizer: t,
		bos:       bosTokenID,
	}, nil
}

func getAddBosTokenString(tokenizerConfigPath string) *string {
	tokenizerConfig, err := os.ReadFile(tokenizerConfigPath)
	if err != nil {
		return nil
	}
	var tokenizerConfigMap map[string]any
	if err := json.Unmarshal(tokenizerConfig, &tokenizerConfigMap); err != nil {
		return nil
	}

	addBosToken, ok := tokenizerConfigMap["add_bos_token"]
	if !ok {
		return nil
	}

	if b, ok := addBosToken.(bool); !ok || !b {
		return nil
	}

	bosTokenValue, ok := tokenizerConfigMap["bos_token"]
	if !ok {
		return nil
	}

	bosToken, ok := bosTokenValue.(string)
	if !ok {
		return nil
	}

	return &bosToken
}
