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

package logs

import (
	"context"
	"time"

	"github.com/go-kit/kit/endpoint"
	"github.com/go-kit/kit/log" //nolint:staticcheck
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
)

// BaseEndpointLogger returns an endpoint middleware that logs the
// duration of each invocation, and the resulting error, if any.
func BaseEndpointLogger(logger log.Logger) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (response interface{}, err error) {
			defer func(begin time.Time) {
				_ = logger.Log("transport_error", err, "took", time.Since(begin))
			}(time.Now())
			return next(ctx, request)
		}
	}
}

// KV - Convenient key-value pairs to pass to loggers
type KV struct {
	kv map[string]interface{}
}

func (kv *KV) With(key string, val interface{}) *KV {
	if kv.kv == nil {
		kv.kv = map[string]interface{}{}
	}
	kv.kv[key] = val
	return kv
}

// ZapEndpointLogger - configures a endpoint logger based on zap logger
func ZapEndpointLogger(kv *KV) endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (response interface{}, err error) {
			defer func(begin time.Time) {
				var fields []zap.Field
				fields = append(fields, zap.Duration("took", time.Since(begin)))
				// Include any (key, value) pairs passed for logging
				for k, v := range kv.kv {
					fields = append(fields, zap.Any(k, v))
				}
				fields = append(fields, zap.Error(err))
				fields = append(fields, logs.GetZapFieldsFromContext(ctx)...)
				if err != nil {
					fields = append(fields, logs.GetZapFieldsForError(err)...)
					zap.L().Error("", fields...)
				} else {
					zap.L().Info("", fields...)
				}
			}(time.Now())
			return next(ctx, request)
		}
	}
}
