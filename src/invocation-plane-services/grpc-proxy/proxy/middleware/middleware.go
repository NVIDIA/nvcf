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
package middleware

import (
	"net/http"
	"time"

	"github.com/MadAppGang/httplog"
	lzap "github.com/MadAppGang/httplog/zap"
	"github.com/go-chi/cors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
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

var DefaultCorsOptions = cors.Options{
	AllowOriginFunc: func(r *http.Request, origin string) bool {
		return true // allow all origins, but in a way that is compatible with AllowCredentials
	},
	AllowedMethods: []string{
		http.MethodHead,
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	},
	AllowedHeaders:   []string{"*"},
	AllowCredentials: true,
	MaxAge:           int(time.Hour.Seconds()),
}

// Cors middleware will ONLY handle pre-flight OPTIONS requests. For all other requests it does not
// add CORS headers. If upstream inference containers wish to support CORS then they have that
// option to handle it themselves. If we were able to send an authenticated CORS request to the
// upstream inference containers then we wouldn't handle those either but since pre-flight CORS
// requests cannot have authentication we have to respond on behalf of the inference containers.
func Cors(next http.Handler) http.Handler {
	corsHandler := cors.Handler(DefaultCorsOptions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this handler should never actually be called. if it is,
		// it means the CORS handler was called for a non-CORS request.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// this is the same guard used by cors.Handler to check for a CORS request
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			corsHandler.ServeHTTP(w, r)
		} else {
			next.ServeHTTP(w, r)
		}
	})
}
