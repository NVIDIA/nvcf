/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package middleware

import (
	"net/http"

	"github.com/MadAppGang/httplog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	lzap "github.com/MadAppGang/httplog/zap"
)

var spanNameFormatter = otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
	return r.URL.Path
})

func ApplyMiddleware(handler http.Handler, routerName string) http.Handler {
	logger := LoggingMiddleware(routerName)
	tracer := TracingMiddleware()
	return logger(tracer(handler))
}

func TracingMiddleware() func(http.Handler) http.Handler {
	return otelhttp.NewMiddleware("", spanNameFormatter)
}

func LoggingMiddleware(routerName string) func(http.Handler) http.Handler {
	return httplog.LoggerWithConfig(
		httplog.LoggerConfig{
			Formatter:  lzap.ZapLogger(zap.L(), zap.InfoLevel, "http request"),
			RouterName: routerName,
		},
	)
}

func TracedRoundTripper(rt http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(rt, spanNameFormatter)
}
