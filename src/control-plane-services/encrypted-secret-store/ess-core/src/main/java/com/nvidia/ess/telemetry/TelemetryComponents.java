/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.ess.telemetry;

import org.springframework.web.server.ServerWebExchange;
import reactor.util.context.ContextView;

/**
 * 1. Span attribute and error recording via {@link ServerWebExchange} - controllers, filters, exception
 * handlers. Avoids setting the attributes on the Spring security filter spans and sets on the Server span instead (expected)
 * 2. For others, {@link ContextView}
 */
public interface TelemetryComponents {

    void setSpanAttribute(ServerWebExchange exchange, String key, String value);

    void setSpanAttribute(ServerWebExchange exchange, String key, long value);

    void setSpanAttribute(ServerWebExchange exchange, String key, boolean value);

    void recordException(ServerWebExchange exchange, Throwable throwable);

    void recordExceptionWithoutErrorStatus(ServerWebExchange exchange, Throwable throwable);

    void setSpanStatusOk(ServerWebExchange exchange);

    void setSpanAttribute(ContextView ctx, String key, String value);

    void setSpanAttribute(ContextView ctx, String key, long value);

    void setSpanAttribute(ContextView ctx, String key, boolean value);

    void recordException(ContextView ctx, Throwable throwable);

    void recordExceptionWithoutErrorStatus(ContextView ctx, Throwable throwable);

    void setSpanStatusOk(ContextView ctx);
}
