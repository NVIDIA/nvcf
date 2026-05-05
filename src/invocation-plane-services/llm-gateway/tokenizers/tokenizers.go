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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	zlog "github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
)

var (
	ErrTokenizerNotFound       = errors.New("tokenizer not found")
	ErrIncompleteUTF8Character = errors.New("incomplete utf-8 character")
)

type Tokenizer interface {
	Encode(text string) ([]uint32, error)
	Decode(ids []uint32, skipSpecial bool) (string, error)
}

type TokenizerStore struct {
	tokenizerPath string
	cacheSize     int
	tokenizers    sync.Map
}

func (ts *TokenizerStore) IsSupportedModel(model string) bool {
	_, err := ts.TokenizerForModel(model)
	return err == nil
}

// TODO: caching is currently disabled.  Is caching actually helpful?
// Might depend on 429 rate for model.
func NewTokenizerStore(
	tokenizerPath string,
	cacheSize int,
	preloadModels []string,
) (*TokenizerStore, error) {
	ts := &TokenizerStore{
		tokenizerPath: tokenizerPath,
		cacheSize:     cacheSize,
	}

	err := ts.preloadTokenizers(preloadModels)
	if err != nil {
		return nil, err
	}

	return ts, nil
}

func (ts *TokenizerStore) preloadTokenizers(preloadModels []string) error {
	zlog.Info().
		Int("num_tokenizers", len(preloadModels)).
		Msg("preloading tokenizers")

	g := new(errgroup.Group)

	for _, model := range preloadModels {
		model := model // capture loop variable
		g.Go(func() error {
			_, err := ts.TokenizerForModel(model)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}

func (ts *TokenizerStore) TokenizerForModel(model string) (Tokenizer, error) {
	if tk, ok := ts.tokenizers.Load(model); ok {
		return must.As[Tokenizer](tk), nil
	}

	tk, err := ts.loadTokenizer(model)
	if err == nil {
		ts.tokenizers.Store(model, tk)
	}
	return tk, err
}

func (ts *TokenizerStore) loadTokenizer(model string) (Tokenizer, error) {
	var (
		start        = time.Now()
		jsonPath     = filepath.Join(ts.tokenizerPath, model, "tokenizer.json")
		tiktokenPath = filepath.Join(ts.tokenizerPath, model, "tiktoken.model")
	)

	if model == "harmony" {
		tokenizer, err := NewHarmonyTokenizer()
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %s", ErrTokenizerNotFound, model, err)
		}
		return tokenizer, nil
	}
	if _, err := os.Stat(tiktokenPath); err == nil {
		tokenizer, err := NewTikTokenTokenizer(filepath.Dir(tiktokenPath))
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %s", ErrTokenizerNotFound, model, err)
		}
		return tokenizer, nil
	} else if _, err := os.Stat(jsonPath); err == nil {
		tokenizer, err := NewHFTokenizer(jsonPath)
		if err != nil {
			zlog.Error().Err(err).Str("model", model).Msg("tokenizerStore: failed to load tokenizer")
			return nil, fmt.Errorf("%w: %s: %s", ErrTokenizerNotFound, model, err)
		}
		zlog.Info().
			Dur("loadDuration", time.Since(start)).
			Str("model", model).
			Str("type", "hf").
			Msg("tokenizerStore: loaded tokenizer")
		return tokenizer, nil
	}

	zlog.Error().
		Str("model", model).
		Str("type", "unknown").
		Msg("tokenizerStore: failed to load tokenizer")
	return nil, fmt.Errorf("%w: %s", ErrTokenizerNotFound, model)
}

func (ts *TokenizerStore) CachingTokenizerForModel(
	model string,
) (*CachingTokenizer, error) {
	tk, err := ts.TokenizerForModel(model)
	if err != nil {
		return nil, err
	}

	return CachingTokenizerForModel(model, tk, ts.cacheSize)
}
