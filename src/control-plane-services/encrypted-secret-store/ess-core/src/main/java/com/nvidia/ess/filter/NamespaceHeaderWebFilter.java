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
package com.nvidia.ess.filter;


import static com.nvidia.ess.constants.Constants.UNKNOWN_NAMESPACE;
import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.NAMESPACE_KEY;
import static com.nvidia.ess.metrics.CustomMetricsRegistry.NAMESPACE_TAG;

import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.ExceptionUtils;
import lombok.Setter;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.security.web.server.firewall.ServerExchangeRejectedException;
import org.springframework.stereotype.Component;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import reactor.util.context.Context;
import tools.jackson.databind.ObjectMapper;

// Marking the attribute on the span as early as possible before the Controllers
@Component
public class NamespaceHeaderWebFilter implements WebFilter {

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    @Setter(onMethod_ = {@Autowired})
    private CustomExceptionHandler errorHandler;

    @Setter(onMethod_ = {@Autowired})
    private ObjectMapper objectMapper;

    @Override
    public Mono<Void> filter(ServerWebExchange exchange, WebFilterChain chain) {
        String namespace;
        try {
            namespace = exchange.getRequest().getHeaders().getFirst(X_ESS_NAMESPACE_HEADER);
        } catch (ServerExchangeRejectedException ex) {
            return errorHandler.handleServerExchangeRejectedException(ex, exchange)
                    .flatMap(responseEntity ->
                            ExceptionUtils.writeErrorResponse(exchange, responseEntity, objectMapper));
        }

        if (StringUtils.isBlank(namespace)) {
            // TODO trying to debug a weird issue in lightstep - a lot of requests with the header set are not populating the span attribute
            // 1. either the header is not available at run time for
            // 2. "current" span is not being used correctly (NOOP span or another request)
            namespace = UNKNOWN_NAMESPACE;
        }

        telemetryComponents.setSpanAttribute(exchange, NAMESPACE_KEY, namespace);

        return chain.filter(exchange)
                // !! very important: propagating in context allows cross-thread usage for metrics
                .contextWrite(Context.of(NAMESPACE_TAG, namespace));
    }
}
