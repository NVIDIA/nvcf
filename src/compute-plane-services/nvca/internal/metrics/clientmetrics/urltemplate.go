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

package clientmetrics

import "context"

type urlTemplateKey struct{}

// ContextWithURLTemplate returns a context carrying the semconv url.template
// (route shape) for requests made with it. A client that serves multiple routes
// through one shared transport sets this per request so the metrics RoundTripper
// can label each call with its own low-cardinality route shape. The template must
// never contain raw IDs or query values.
func ContextWithURLTemplate(ctx context.Context, template string) context.Context {
	return context.WithValue(ctx, urlTemplateKey{}, template)
}

// URLTemplateFromContext returns the url.template set on ctx, or "" if none.
func URLTemplateFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(urlTemplateKey{}).(string); ok {
		return v
	}
	return ""
}
