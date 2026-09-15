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

import static com.nvidia.ess.constants.Constants.MDC_AGENT_ID_KEY;
import static com.nvidia.ess.constants.Constants.X_ESS_AGENT_ID_HEADER;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.AGENT_ID_KEY;

import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import com.nvidia.ess.utils.ExceptionUtils;
import lombok.Setter;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication.Type;
import org.springframework.security.web.server.firewall.ServerExchangeRejectedException;
import org.springframework.stereotype.Component;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import reactor.util.context.Context;
import tools.jackson.databind.ObjectMapper;

@Component
@ConditionalOnWebApplication(type = Type.REACTIVE)
public class EssAgentIdWebFilter implements WebFilter {

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    @Setter(onMethod_ = {@Autowired})
    private CustomExceptionHandler errorHandler;

    @Setter(onMethod_ = {@Autowired})
    private ObjectMapper objectMapper;

    @Override
    public Mono<Void> filter(ServerWebExchange exchange, WebFilterChain chain) {
        String agentId;
        try {
            agentId = exchange.getRequest().getHeaders().getFirst(X_ESS_AGENT_ID_HEADER);
        } catch (ServerExchangeRejectedException ex) {
            return errorHandler.handleServerExchangeRejectedException(ex, exchange)
                    .flatMap(responseEntity ->
                            ExceptionUtils.writeErrorResponse(exchange, responseEntity, objectMapper));
        }

        // agent id should only be populated on Secret GET by the ESS agent
        // no need to generate defaults
        if (agentId == null) {
            return chain.filter(exchange);
        }
        telemetryComponents.setSpanAttribute(exchange, AGENT_ID_KEY, agentId);
        exchange.getResponse().getHeaders().add(X_ESS_AGENT_ID_HEADER, agentId);

        return chain.filter(exchange)
                // populate initial reactive context for ThreadLocalAccessor to propagate
                .contextWrite(Context.of(MDC_AGENT_ID_KEY, agentId));
    }
}
