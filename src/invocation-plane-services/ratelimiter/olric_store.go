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

package ratelimiter

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/olric-data/olric"
	"github.com/ulule/limiter/v3"
	"github.com/ulule/limiter/v3/drivers/store/common"
)

// Client is an interface that allows using an Olric client.
type Client interface {
	NewDMap(name string, options ...olric.DMapOption) (olric.DMap, error)
}

// Store is the Olric store.
type Store struct {
	// Prefix used for the key.
	Prefix string
	// dmap is the distributed map instance.
	dmap olric.DMap
}

// GetDMap returns the underlying Olric DMap for metrics collection
func (store *Store) GetDMap() olric.DMap {
	return store.dmap
}

// NewStore returns an instance of Olric store with defaults.
func NewStore(client Client) (limiter.Store, error) {
	return NewStoreWithOptions(client, limiter.StoreOptions{
		Prefix: limiter.DefaultPrefix,
	})
}

// NewStoreWithOptions returns an instance of Olric store with options.
func NewStoreWithOptions(client Client, options limiter.StoreOptions) (limiter.Store, error) {
	dmap, err := client.NewDMap(options.Prefix)
	if err != nil {
		return nil, err
	}

	store := &Store{
		Prefix: options.Prefix,
		dmap:   dmap,
	}

	return store, nil
}

// Increment increments the limit by the given count & gives back the new limit for the given identifier.
// NOT USED
func (store *Store) Increment(ctx context.Context, key string, count int64, rate limiter.Rate) (limiter.Context, error) {
	return limiter.Context{}, errors.ErrUnsupported
}

// Get returns the limit for the given identifier.
func (store *Store) Get(ctx context.Context, key string, rate limiter.Rate) (limiter.Context, error) {
	// This logic is from drivers/store/redis/store.go
	// Olric operations(expire, increment, put) all eventually go down to Put. Thus, this is all written with Put

	tracer := otel.Tracer("Olric")
	ctx, span := tracer.Start(ctx, "Olric-get")
	defer span.End()
	span.SetAttributes(attribute.String("key", key))

	fullKey := store.getCacheKey(key)
	result, err := store.dmap.Get(ctx, fullKey)
	if err != nil && !errors.Is(err, olric.ErrKeyNotFound) {
		zap.L().Error("Failed to get value", zap.Error(err))
		return limiter.Context{}, err
	}
	value := 1
	// initialize the value to 1 if key doesn't exist
	if errors.Is(err, olric.ErrKeyNotFound) {
		err = store.dmap.Put(ctx, fullKey, 1)
		if err != nil {
			return limiter.Context{}, err
		}
	} else {
		if result != nil && result.TTL() > 0 {
			// update the existing value by incrementing 1
			old, _ := result.Int()
			value = old + 1
			putOption := olric.PX(time.Until(time.UnixMilli(result.TTL())))
			err := store.dmap.Put(ctx, fullKey, value, putOption)
			if err != nil {
				zap.L().Error("Failed to update value", zap.Error(err))
				return limiter.Context{}, err
			}
		} else {
			// when ttl is 0, reset the value to 1
			err = store.dmap.Put(ctx, fullKey, 1)
			if err != nil {
				zap.L().Error("Failed to update value", zap.Error(err))
				return limiter.Context{}, err
			}
		}
	}
	span.SetAttributes(attribute.String("value", strconv.Itoa(value)))

	now := time.Now()
	expiration := now.Add(rate.Period)
	if value == 1 {
		if rate.Period.Milliseconds() > 0 {
			ttlOption := olric.PX(rate.Period)
			err = store.dmap.Put(ctx, fullKey, value, ttlOption)

			// ignore key not found error
			if err != nil && !errors.Is(err, olric.ErrKeyNotFound) {
				zap.L().Error("Failed to set expiration", zap.Error(err))
				return limiter.Context{}, err
			}
		}
		return common.GetContextFromState(now, rate, expiration, int64(value)), nil
	}

	if err != nil && !errors.Is(err, olric.ErrKeyNotFound) {
		zap.L().Error("Failed to get remaining ttl", zap.Error(err))
		return limiter.Context{}, err
	}

	if result.TTL() > 0 {
		expiration = now.Add(time.Duration(result.TTL()) * time.Millisecond)
	}
	span.SetAttributes(attribute.String("expiration", expiration.String()))
	return common.GetContextFromState(now, rate, expiration, int64(value)), nil
}

// Peek returns the limit for the given identifier, without modification on current values.
// NOT USED
func (store *Store) Peek(ctx context.Context, key string, rate limiter.Rate) (limiter.Context, error) {
	return limiter.Context{}, errors.ErrUnsupported
}

// Reset returns the limit for the given identifier which is set to zero.
func (store *Store) Reset(ctx context.Context, key string, rate limiter.Rate) (limiter.Context, error) {
	fullKey := store.getCacheKey(key)
	_, err := store.dmap.Delete(ctx, fullKey)
	if err != nil {
		return limiter.Context{}, err
	}

	now := time.Now()
	expiration := now.Add(rate.Period)

	return common.GetContextFromState(now, rate, expiration, 0), nil
}

// getCacheKey returns the full path for an identifier.
func (store *Store) getCacheKey(key string) string {
	return store.Prefix + ":" + key
}
