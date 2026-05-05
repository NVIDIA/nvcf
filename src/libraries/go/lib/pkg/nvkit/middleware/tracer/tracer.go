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

package tracer

import (
	"context"

	"github.com/go-kit/kit/endpoint"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/status"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/api/errors/v1"
)

// ErrorTracer middle sets error details in the span so it can be used to differentiate different errors from span
func ErrorTracer() endpoint.Middleware {
	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (response interface{}, err error) {
			defer func() {
				if err != nil {
					nvErr := status.Convert(err)
					httpStatus := runtime.HTTPStatusFromCode(nvErr.Code())
					if span := trace.SpanFromContext(ctx); span != nil {
						defer span.End()
						for _, d := range nvErr.Details() {
							switch info := d.(type) { //nolint:gocritic
							case *nverrors.NVError:
								span.SetStatus(codes.Error, nvErr.Message())
								span.SetAttributes(attribute.String("error.id", info.ErrorId))
							}
						}
						span.SetAttributes(attribute.Int("http.statusCode", httpStatus))
					}
				}
			}()
			return next(ctx, request)
		}
	}
}
