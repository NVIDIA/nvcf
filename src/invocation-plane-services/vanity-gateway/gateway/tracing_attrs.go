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

package gateway

import "go.opentelemetry.io/otel/attribute"

const (
	traceAttrEndpointType              attribute.Key = "endpoint.type"
	traceAttrFunctionID                attribute.Key = "function.id"
	traceAttrFunctionVersionID         attribute.Key = "function.version_id"
	traceAttrHTTPResponseStatusCode    attribute.Key = "http.response.status_code"
	traceAttrIsShadow                  attribute.Key = "is_shadow"
	traceAttrModelName                 attribute.Key = "model.name"
	traceAttrModelStream               attribute.Key = "model.stream"
	traceAttrSessionTimeoutSeconds     attribute.Key = "session_timeout.seconds"
	traceAttrShadowDispatched          attribute.Key = "shadow.dispatched"
	traceAttrShadowDispatchedCount     attribute.Key = "shadow.dispatched_count"
	traceAttrShadowDroppedCount        attribute.Key = "shadow.dropped_count"
	traceAttrShadowDroppedReasons      attribute.Key = "shadow.dropped_reasons"
	traceAttrShadowDroppedTargetModels attribute.Key = "shadow.dropped_target_models"
	traceAttrShadowTargetModel         attribute.Key = "shadow.target_model"
	traceAttrShadowTargetModels        attribute.Key = "shadow.target_models"

	traceAttrValueEndpointOpenAI = "openai"

	shadowDroppedReasonBodyReadError    = "body_read_error"
	shadowDroppedReasonBodyRewriteError = "body_rewrite_error"
	shadowDroppedReasonConcurrencyLimit = "concurrency_limit"
	shadowReplaySpanName                = "shadow_replay"
)
