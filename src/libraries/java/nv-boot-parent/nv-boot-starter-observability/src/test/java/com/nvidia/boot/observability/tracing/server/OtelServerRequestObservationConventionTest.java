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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.util.Map;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.http.server.observation.ServerRequestObservationContext;

class OtelServerRequestObservationConventionTest {

    private OtelServerRequestObservationConvention convention;
    private HttpServletRequest request;
    private HttpServletResponse response;
    private ServerRequestObservationContext context;

    @BeforeEach
    void setUp() {
        convention = new OtelServerRequestObservationConvention();
        request = mock(HttpServletRequest.class);
        response = mock(HttpServletResponse.class);
        context = new ServerRequestObservationContext(request, response);
    }

    @Test
    @DisplayName("Low cardinality key values include http.request.method")
    void lowCardinalityIncludesHttpMethod() {
        when(request.getMethod()).thenReturn("GET");
        when(response.getStatus()).thenReturn(200);

        var keyValues = convention.getLowCardinalityKeyValues(context);
        var map = toMap(keyValues);

        assertThat(map).containsEntry("http.request.method", "GET");
        assertThat(map).containsEntry("http.response.status_code", "200");
        assertThat(map).containsEntry("http.status_code_family", "2xx");
    }

    @Test
    @DisplayName("Low cardinality records status from getStatus() even when the response is not yet committed (e.g., void handler with @ResponseStatus(NO_CONTENT))")
    void lowCardinalityRecordsStatusForUncommittedResponse() {
        when(request.getMethod()).thenReturn("DELETE");
        when(response.getStatus()).thenReturn(204);

        var keyValues = convention.getLowCardinalityKeyValues(context);
        var map = toMap(keyValues);

        assertThat(map).containsEntry("http.response.status_code", "204");
        assertThat(map).containsEntry("http.status_code_family", "2xx");
    }

    @Test
    @DisplayName("High cardinality key values include url.path")
    void highCardinalityIncludesUrlPath() {
        when(request.getRequestURI()).thenReturn("/api/health");
        when(request.getScheme()).thenReturn("https");

        var keyValues = convention.getHighCardinalityKeyValues(context);
        var map = toMap(keyValues);

        assertThat(map).containsEntry("url.path", "/api/health");
        assertThat(map).containsEntry("url.scheme", "https");
    }

    @Test
    @DisplayName("Contextual name follows OTel Java agent style (METHOD /route), not http lowercase")
    void contextualNameMatchesOpenTelemetryHttpSpanNaming() {
        when(request.getMethod()).thenReturn("POST");
        context.setPathPattern("/v1/nvct/tasks");

        assertThat(convention.getContextualName(context)).isEqualTo("POST /v1/nvct/tasks");
    }

    @Test
    @DisplayName("Contextual name is method only when route is unknown")
    void contextualNameMethodOnlyWithoutRoute() {
        when(request.getMethod()).thenReturn("GET");

        assertThat(convention.getContextualName(context)).isEqualTo("GET");
    }

    @Test
    @DisplayName("Low cardinality always emits consistent tag keys even when route/status/error are absent")
    void lowCardinalityAlwaysEmitsConsistentKeys() {
        when(request.getMethod()).thenReturn("GET");
        var ctx = new ServerRequestObservationContext(request, null);

        var keyValues = convention.getLowCardinalityKeyValues(ctx);
        var map = toMap(keyValues);

        var expectedKeys = Set.of(
                "http.request.method", "http.response.status_code", "http.status_code_family",
                "http.route", "outcome", "exception.type");
        assertThat(map.keySet()).containsAll(expectedKeys);

        assertThat(map).containsEntry("http.response.status_code", "none");
        assertThat(map).containsEntry("http.status_code_family", "none");
        assertThat(map).containsEntry("http.route", "none");
        assertThat(map).containsEntry("exception.type", "none");
    }

    private static Map<String, String> toMap(KeyValues keyValues) {
        return keyValues.stream()
                .collect(Collectors.toMap(KeyValue::getKey, KeyValue::getValue));
    }
}
