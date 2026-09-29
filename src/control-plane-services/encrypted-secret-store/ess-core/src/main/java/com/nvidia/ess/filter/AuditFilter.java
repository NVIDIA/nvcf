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

import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication.Type;
import org.springframework.stereotype.Service;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import tools.jackson.databind.ObjectMapper;


@Slf4j
@Service
@ConditionalOnWebApplication(type = Type.REACTIVE)
public class AuditFilter implements WebFilter {

    private static final String DEFAULT_ACTOR_ID = "unknown";

    private final AuditService auditService;
    private final ObjectMapper objectMapper;

    public AuditFilter(@NonNull AuditService auditService, @NonNull ObjectMapper objectMapper) {
        this.auditService = auditService;
        this.objectMapper = objectMapper;
    }

    @Override
    public Mono<Void> filter(ServerWebExchange exchange, WebFilterChain chain) {
        String path = exchange.getRequest().getPath().value();

        if (!path.startsWith("/v1/")) {
            return chain.filter(exchange);
        }

        return chain.filter(exchange)
                .doOnSuccess(aVoid -> logAuthResult(exchange));
    }

    private void logAuthResult(ServerWebExchange exchange) {
        AuthorizationInfo authInfo = (AuthorizationInfo) exchange.getAttributes().get("authInfo");

        String actor = DEFAULT_ACTOR_ID;
        String subject = DEFAULT_ACTOR_ID;
        if (authInfo != null) {
            actor = authInfo.getIss() + "_" + authInfo.getId();
            subject = authInfo.getId();
        }

        String remoteAddress = exchange.getRequest().getRemoteAddress() != null
                ? exchange.getRequest().getRemoteAddress().getHostName() : "";

        try {
            var statusCode = exchange.getResponse().getStatusCode();
            String summary = statusCode != null ? statusCode.toString() : "No Status Code available";

            AuditEventPayload.Builder payloadBuilder = auditService.auditEventPayloadBuilder()
                    .operation(exchange.getRequest().getMethod().name())
                    .type("API")
                    .actorId(actor)
                    .subjectId(subject)
                    .actorLocation(remoteAddress)
                    .subjectLocation(remoteAddress)
                    .objectLocation(exchange.getRequest().getURI().toString())
                    .jsonBefore(objectMapper.readTree("{\"request\": \"started\"}"))
                    .jsonAfter(objectMapper.readTree("{\"request\": \"completed\"}"))
                    .objectId("N/A")
                    .state("N/A")
                    .summary(summary);

            auditService.audit(payloadBuilder);
        } catch (Exception ex) {
            log.error("Audit logs exception", ex);
        }
    }
}
