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

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.databind.DatabindException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.boot.audit.AuditService;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.ess.controller.response.AuthorizationInfo;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.URI;
import java.util.Collections;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.mockito.Captor;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.server.RequestPath;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.http.server.reactive.ServerHttpResponse;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

class AuditFilterTest {

    @Mock
    private AuditService auditService;

    @Mock
    private ObjectMapper objectMapper;

    @Mock
    private AuditEventPayload.Builder payloadBuilder;

    @Mock
    private ServerWebExchange exchange;

    @Mock
    private WebFilterChain chain;

    @Captor
    private ArgumentCaptor<AuditEventPayload.Builder> builderCaptor;

    private AuditFilter auditFilter;

    private static final String DEFAULT_ACTOR_ID = "unknown";

    @BeforeEach
    void setUp() throws IOException {
        MockitoAnnotations.openMocks(this);

        JsonNode mockJsonNode = mock(ObjectNode.class);
        when(objectMapper.readTree("{\"request\": \"started\"}")).thenReturn(mockJsonNode);
        when(objectMapper.readTree("{\"request\": \"completed\"}")).thenReturn(mockJsonNode);

        when(auditService.auditEventPayloadBuilder()).thenReturn(payloadBuilder);
        when(payloadBuilder.operation(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.type(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.actorId(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.subjectId(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.actorLocation(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.subjectLocation(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.objectLocation(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.jsonBefore(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.jsonAfter(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.objectId(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.state(any())).thenReturn(payloadBuilder);
        when(payloadBuilder.summary(any())).thenReturn(payloadBuilder);

        when(chain.filter(exchange)).thenReturn(Mono.empty());

        ServerHttpRequest request = mock(ServerHttpRequest.class);
        when(exchange.getRequest()).thenReturn(request);
        when(request.getMethod()).thenReturn(HttpMethod.GET);
        when(request.getRemoteAddress()).thenReturn(new InetSocketAddress("localhost", 8080));
        when(request.getURI()).thenReturn(URI.create("http://localhost:8080/v1/test"));

        RequestPath requestPath = mock(RequestPath.class);
        when(request.getPath()).thenReturn(requestPath);
        when(requestPath.value()).thenReturn("/v1/test");

        ServerHttpResponse response = mock(ServerHttpResponse.class);
        when(exchange.getResponse()).thenReturn(response);
        when(response.getStatusCode()).thenReturn(HttpStatus.OK);

        auditFilter = new AuditFilter(auditService, objectMapper);
    }

    @Test
    void filter_shouldLogAuthResult_whenAuthInfoIsPresent() {
        AuthorizationInfo authInfo = new AuthorizationInfo("id", "name", "iss", "jwks");
        when(exchange.getAttributes()).thenReturn(Collections.singletonMap("authInfo", authInfo));

        StepVerifier.create(auditFilter.filter(exchange, chain)).verifyComplete();

        verify(auditService, times(1)).audit(builderCaptor.capture());
        verify(payloadBuilder).actorId("iss_id");
        verify(payloadBuilder).subjectId("id");
    }

    @Test
    void filter_shouldSkipLogging_whenPathDoesNotStartWithV1() {
        ServerHttpRequest request = mock(ServerHttpRequest.class);
        when(exchange.getRequest()).thenReturn(request);
        when(request.getURI()).thenReturn(URI.create("http://localhost:8080/open"));

        RequestPath requestPath = mock(RequestPath.class);
        when(request.getPath()).thenReturn(requestPath);
        when(requestPath.value()).thenReturn("/open");

        StepVerifier.create(auditFilter.filter(exchange, chain)).verifyComplete();

        verify(auditService, never()).audit(any());
    }

    @Test
    void filter_shouldUnknown_whenAuthInfoIsNotPresent() {
        when(exchange.getAttributes()).thenReturn(Collections.emptyMap());

        StepVerifier.create(auditFilter.filter(exchange, chain)).verifyComplete();

        verify(auditService, times(1)).audit(builderCaptor.capture());
        verify(payloadBuilder).actorId(DEFAULT_ACTOR_ID);
        verify(payloadBuilder).subjectId(DEFAULT_ACTOR_ID);
    }

    @Test
    void filter_shouldHandleJsonParsingException() throws IOException {
        AuthorizationInfo authInfo = new AuthorizationInfo("id", "name", "iss", "jwks");
        when(exchange.getAttributes()).thenReturn(Collections.singletonMap("authInfo", authInfo));

        when(objectMapper.readTree("{\"request\": \"started\"}")).thenThrow(DatabindException.from((tools.jackson.core.JsonParser) null, "JSON parsing error"));

        StepVerifier.create(auditFilter.filter(exchange, chain)).verifyComplete();

        verify(auditService, never()).audit(any());
    }
}
