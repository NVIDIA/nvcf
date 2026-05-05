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
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/llm-api-gateway/cache"
	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
	"github.com/NVIDIA/nvcf/llm-api-gateway/util"
)

var (
	cachingTokenizers   sync.Map
	keyedTokenizerLocks = util.NewKeyLocks()
)

const (
	ttlShortEncoding = 24 * time.Hour
	ttlLongEncoding  = 10 * time.Minute
)

type cacheKey string

func (ck cacheKey) String() string {
	return string(ck)
}

type CachingTokenizer struct {
	model     string
	tokenizer Tokenizer
	cache     cache.Cache[cacheKey, []uint32]
}

func CachingTokenizerForModel(
	model string,
	tk Tokenizer,
	cacheSize int,
) (*CachingTokenizer, error) {
	if cachedTokenizer, ok := cachingTokenizers.Load(model); ok {
		return must.As[*CachingTokenizer](cachedTokenizer), nil
	}

	keyedTokenizerLocks.Lock(model)
	defer keyedTokenizerLocks.Unlock(model)

	// make sure tokenizer wasn't created while we were waiting for the lock
	if cachedTokenizer, ok := cachingTokenizers.Load(model); ok {
		return must.As[*CachingTokenizer](cachedTokenizer), nil
	}

	ct := &CachingTokenizer{
		model:     model,
		tokenizer: tk,
		cache:     cache.WrapCache[cacheKey, []uint32](cacheSize),
	}

	cachingTokenizers.Store(model, ct)
	return ct, nil
}

func (ct *CachingTokenizer) Tokenize(text string) ([]uint32, error) {
	if encoding, ok := ct.cache.Get(cacheKey(text)); ok {
		return encoding, nil
	}

	encoding, err := ct.tokenizer.Encode(text)
	if err != nil {
		return nil, err
	}

	ttlEncoding := ttlShortEncoding
	if len(encoding) > 64 {
		ttlEncoding = ttlLongEncoding
	}

	ct.cache.Set(cacheKey(text), encoding, ttlEncoding)
	return encoding, nil
}
