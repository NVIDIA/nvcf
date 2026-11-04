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

package lastcluster

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olric-data/olric"
)

// OlricStore keeps entries in an Olric DMap shared by every gateway replica
// in the Olric cluster.
type OlricStore struct {
	dmap olric.DMap
}

func NewOlricStore(dmap olric.DMap) *OlricStore {
	return &OlricStore{dmap: dmap}
}

func (s *OlricStore) Get(ctx context.Context, key string) (string, bool, error) {
	resp, err := s.dmap.Get(ctx, key)
	if err != nil {
		if errors.Is(err, olric.ErrKeyNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("olric get: %w", err)
	}
	value, err := resp.String()
	if err != nil {
		return "", false, fmt.Errorf("olric get decode: %w", err)
	}
	return value, true, nil
}

func (s *OlricStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := s.dmap.Put(ctx, key, value, olric.PX(ttl)); err != nil {
		return fmt.Errorf("olric put: %w", err)
	}
	return nil
}
