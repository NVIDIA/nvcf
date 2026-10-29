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

package com.nvidia.boot.observability.tracing.server;

import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Optional;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.server.reactive.observation.DefaultServerRequestObservationConvention;
import org.springframework.http.server.reactive.observation.ServerRequestObservationContext;

/**
 * Reactive (WebFlux) variant of {@link OtelServerRequestObservationConvention}: Emits OpenTelemetry
 * semantic convention attribute names for HTTP spans and uses OTel-style contextual span names
 * ({@code POST /v1/foo}) instead of Spring's default {@code http post /v1/foo}. This class
 * overrides {@link #getContextualName} to follow that format so that traces generated
 * using Spring Boot 3 + Micrometer do not require changes to the existing Lightstep dashboards.
 *
 * @see OtelServerRequestObservationConvention
 */
public class OtelReactiveServerRequestObservationConvention
        extends DefaultServerRequestObservationConvention {

    private static final String HTTP_REQUEST_METHOD = "http.request.method";
    private static final String HTTP_RESPONSE_STATUS_CODE = "http.response.status_code";
    private static final String HTTP_STATUS_CODE_FAMILY = "http.status_code_family";
    private static final String HTTP_ROUTE = "http.route";
    private static final String URL_PATH = "url.path";
    private static final String URL_SCHEME = "url.scheme";
    private static final String USER_AGENT_ORIGINAL = "user_agent.original";
    private static final String NETWORK_PEER_ADDRESS = "network.peer.address";
    private static final String NETWORK_PEER_PORT = "network.peer.port";
    private static final String SERVER_ADDRESS = "server.address";
    private static final String SERVER_PORT = "server.port";
    private static final String CLIENT_ADDRESS = "client.address";
    private static final String EXCEPTION_TYPE = "exception.type";
    private static final String THREAD_ID = "thread.id";
    private static final String THREAD_NAME = "thread.name";

    public OtelReactiveServerRequestObservationConvention() {
        super();
    }

    /**
     * Use OpenTelemetry HTTP span naming ({@code POST /api/items}) instead of Spring's default
     * {@code http post /api/items}.
     */
    @Override
    public String getContextualName(ServerRequestObservationContext context) {
        var request = context.getCarrier();
        if (request == null) {
            return super.getContextualName(context);
        }
        var method = request.getMethod().name();
        var route = context.getPathPattern();
        if (StringUtils.isNotBlank(route)) {
            return method + " " + route;
        }
        return method;
    }

    @Override
    public KeyValues getLowCardinalityKeyValues(ServerRequestObservationContext context) {
        var request = context.getCarrier();
        var response = context.getResponse();
        var keyValues = new ArrayList<KeyValue>();

        keyValues.add(KeyValue.of(HTTP_REQUEST_METHOD, request.getMethod().name()));

        var httpStatus = response != null ? response.getStatusCode() : null;
        if (httpStatus != null) {
            int statusCode = httpStatus.value();
            keyValues.add(KeyValue.of(HTTP_RESPONSE_STATUS_CODE, String.valueOf(statusCode)));
            keyValues.add(KeyValue.of(HTTP_STATUS_CODE_FAMILY, statusCodeFamily(statusCode)));
        } else {
            keyValues.add(KeyValue.of(HTTP_RESPONSE_STATUS_CODE, KeyValue.NONE_VALUE));
            keyValues.add(KeyValue.of(HTTP_STATUS_CODE_FAMILY, KeyValue.NONE_VALUE));
        }

        keyValues.add(KeyValue.of(HTTP_ROUTE,
                Optional.ofNullable(context.getPathPattern()).orElse(KeyValue.NONE_VALUE)));

        keyValues.add(outcome(context));

        var error = context.getError();
        if (error != null) {
            keyValues.add(KeyValue.of(EXCEPTION_TYPE, error.getClass().getName()));
        } else {
            keyValues.add(KeyValue.of(EXCEPTION_TYPE, KeyValue.NONE_VALUE));
        }

        return KeyValues.of(keyValues);
    }

    @Override
    public KeyValues getHighCardinalityKeyValues(ServerRequestObservationContext context) {
        var request = context.getCarrier();
        var keyValues = new ArrayList<KeyValue>();

        var path = request.getPath();
        if (path != null) {
            keyValues.add(KeyValue.of(URL_PATH, path.toString()));
        }

        var uri = request.getURI();
        if (uri != null && uri.getScheme() != null) {
            keyValues.add(KeyValue.of(URL_SCHEME, uri.getScheme()));
        }

        var userAgent = request.getHeaders().getFirst("User-Agent");
        if (StringUtils.isNotBlank(userAgent)) {
            keyValues.add(KeyValue.of(USER_AGENT_ORIGINAL, userAgent));
        }

        var clientAddr = resolveClientAddress(request);
        if (StringUtils.isNotBlank(clientAddr)) {
            keyValues.add(KeyValue.of(NETWORK_PEER_ADDRESS, clientAddr));
            keyValues.add(KeyValue.of(CLIENT_ADDRESS, clientAddr));
        }

        var remote = request.getRemoteAddress();
        if (remote != null && remote.getPort() > 0) {
            keyValues.add(KeyValue.of(NETWORK_PEER_PORT, String.valueOf(remote.getPort())));
        }

        var local = request.getLocalAddress();
        if (local != null) {
            if (local.getHostString() != null && !local.getHostString().isBlank()) {
                keyValues.add(KeyValue.of(SERVER_ADDRESS, local.getHostString()));
            }
            if (local.getPort() > 0) {
                keyValues.add(KeyValue.of(SERVER_PORT, String.valueOf(local.getPort())));
            }
        }

        var thread = Thread.currentThread();
        keyValues.add(KeyValue.of(THREAD_ID, String.valueOf(thread.threadId())));
        var threadName = thread.getName();
        if (StringUtils.isNotBlank(threadName)) {
            keyValues.add(KeyValue.of(THREAD_NAME, threadName));
        }

        return KeyValues.of(keyValues);
    }

    private static String resolveClientAddress(org.springframework.http.server.reactive.ServerHttpRequest request) {
        var forwardedFor = request.getHeaders().getFirst("X-Forwarded-For");
        if (StringUtils.isNotBlank(forwardedFor)) {
            var firstIp = Arrays.stream(forwardedFor.split(","))
                    .map(String::trim)
                    .filter(s -> !s.isBlank())
                    .findFirst();
            if (firstIp.isPresent()) {
                return firstIp.get();
            }
        }
        var realIp = request.getHeaders().getFirst("X-Real-IP");
        if (StringUtils.isNotBlank(realIp)) {
            return realIp.trim();
        }
        var remote = request.getRemoteAddress();
        if (remote != null && remote.getAddress() != null) {
            return remote.getAddress().getHostAddress();
        }
        return null;
    }

    private static String statusCodeFamily(int statusCode) {
        if (statusCode >= 100 && statusCode < 200) {
            return "1xx";
        }
        if (statusCode >= 200 && statusCode < 300) {
            return "2xx";
        }
        if (statusCode >= 300 && statusCode < 400) {
            return "3xx";
        }
        if (statusCode >= 400 && statusCode < 500) {
            return "4xx";
        }
        if (statusCode >= 500 && statusCode < 600) {
            return "5xx";
        }
        return "other";
    }
}
