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
import jakarta.servlet.http.HttpServletRequest;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Optional;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.server.observation.DefaultServerRequestObservationConvention;
import org.springframework.http.server.observation.ServerRequestObservationContext;

/**
 * Custom {@link org.springframework.http.server.observation.ServerRequestObservationConvention}
 * that emits OpenTelemetry semantic convention attribute names for HTTP spans, aligning with
 * what the OpenTelemetry Java agent produces (e.g. http.request.method, url.path, http.route).
 *
 * <p>Spring's default {@link DefaultServerRequestObservationConvention} sets the observation
 * contextual name to {@code http get /path} (literal prefix {@code http } plus lowercase verb).
 * That becomes the OTLP span name and shows up in Lightstep as {@code Operation} like
 * {@code http post /v1/foo}. The OpenTelemetry HTTP semantic conventions use
 * {@code {METHOD} {http.route}} instead (e.g. {@code POST /v1/foo}), matching Java agent behavior.
 * This class overrides {@link #getContextualName} to follow that format so that traces generated
 * using Spring Boot 3 + Micrometer do not require changes to the existing Lightstep dashboards.
 */
public class OtelServerRequestObservationConvention
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

    public OtelServerRequestObservationConvention() {
        super();
    }

    /**
     * Use OpenTelemetry HTTP span naming ({@code POST /v1/nvct/tasks}) instead of Spring's default
     * {@code http post /v1/nvct/tasks}, so backends such as Lightstep show Operations consistent
     * with the OpenTelemetry Java agent.
     */
    @Override
    public String getContextualName(ServerRequestObservationContext context) {
        var request = context.getCarrier();
        if (request == null) {
            return super.getContextualName(context);
        }
        var method = request.getMethod();
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

        keyValues.add(KeyValue.of(HTTP_REQUEST_METHOD, request.getMethod()));

        if (response != null) {
            var statusCode = response.getStatus();
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

        var requestUri = request.getRequestURI();
        if (requestUri != null) {
            keyValues.add(KeyValue.of(URL_PATH, requestUri));
        }

        var scheme = request.getScheme();
        if (scheme != null) {
            keyValues.add(KeyValue.of(URL_SCHEME, scheme));
        }

        var userAgent = request.getHeader("User-Agent");
        if (StringUtils.isNotBlank(userAgent)) {
            keyValues.add(KeyValue.of(USER_AGENT_ORIGINAL, userAgent));
        }

        var clientAddr = resolveClientAddress(request);
        if (StringUtils.isNotBlank(clientAddr)) {
            keyValues.add(KeyValue.of(NETWORK_PEER_ADDRESS, clientAddr));
            keyValues.add(KeyValue.of(CLIENT_ADDRESS, clientAddr));
        }

        var remotePort = request.getRemotePort();
        if (remotePort > 0) {
            keyValues.add(KeyValue.of(NETWORK_PEER_PORT, String.valueOf(remotePort)));
        }

        var serverAddr = resolveServerAddress(request);
        if (StringUtils.isNotBlank(serverAddr)) {
            keyValues.add(KeyValue.of(SERVER_ADDRESS, serverAddr));
        }

        var serverPort = request.getServerPort();
        if (serverPort > 0) {
            keyValues.add(KeyValue.of(SERVER_PORT, String.valueOf(serverPort)));
        }

        var thread = Thread.currentThread();
        keyValues.add(KeyValue.of(THREAD_ID, String.valueOf(thread.threadId())));
        var threadName = thread.getName();
        if (StringUtils.isNotBlank(threadName)) {
            keyValues.add(KeyValue.of(THREAD_NAME, threadName));
        }

        return KeyValues.of(keyValues);
    }

    /**
     * Resolves the client IP address, preferring proxy headers when present.
     * Checks X-Forwarded-For (first IP in the chain), then X-Real-IP, then the direct remote addr.
     */
    private static String resolveClientAddress(HttpServletRequest request) {
        var forwardedFor = request.getHeader("X-Forwarded-For");
        if (StringUtils.isNotBlank(forwardedFor)) {
            var firstIp = Arrays.stream(forwardedFor.split(","))
                    .map(String::trim)
                    .filter(s -> !s.isBlank())
                    .findFirst();
            if (firstIp.isPresent()) {
                return firstIp.get();
            }
        }
        var realIp = request.getHeader("X-Real-IP");
        if (StringUtils.isNotBlank(realIp)) {
            return realIp.trim();
        }
        return request.getRemoteAddr();
    }

    /**
     * Resolves the server address/hostname, preferring proxy headers when present.
     * Checks X-Forwarded-Host, then Host, then the request server name.
     */
    private static String resolveServerAddress(HttpServletRequest request) {
        var forwardedHost = request.getHeader("X-Forwarded-Host");
        if (StringUtils.isNotBlank(forwardedHost)) {
            var firstHost = Arrays.stream(forwardedHost.split(","))
                    .map(String::trim)
                    .filter(s -> !s.isBlank())
                    .findFirst();
            if (firstHost.isPresent()) {
                return firstHost.get();
            }
        }
        var host = request.getHeader("Host");
        if (StringUtils.isNotBlank(host)) {
            var hostWithoutPort = host.split(":")[0].trim();
            if (!hostWithoutPort.isBlank()) {
                return hostWithoutPort;
            }
        }
        return request.getServerName();
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
