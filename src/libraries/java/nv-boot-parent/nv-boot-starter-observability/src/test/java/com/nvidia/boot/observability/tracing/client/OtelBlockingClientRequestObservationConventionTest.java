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

package com.nvidia.boot.observability.tracing.client;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.net.URI;
import java.util.Set;
import java.util.stream.Collectors;
import org.junit.jupiter.api.DisplayName;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.client.ClientHttpRequest;
import org.springframework.http.client.ClientHttpResponse;
import org.springframework.http.client.observation.ClientRequestObservationContext;

class OtelBlockingClientRequestObservationConventionTest {

    private final OtelBlockingClientRequestObservationConvention convention = new OtelBlockingClientRequestObservationConvention();

    @Test
    @DisplayName("Low cardinality includes http.request.method, server.address, http.route, http.response.status_code")
    void lowCardinalityEmitsOtelKeys() throws Exception {
        var request = mock(ClientHttpRequest.class);
        when(request.getMethod()).thenReturn(HttpMethod.POST);
        when(request.getURI()).thenReturn(URI.create("https://api.example.com:8443/v1/items"));

        var context = new ClientRequestObservationContext(request);
        context.setUriTemplate("https://api.example.com:8443/v1/{id}");

        var response = mock(ClientHttpResponse.class);
        when(response.getStatusCode()).thenReturn(HttpStatus.CREATED);
        context.setResponse(response);

        var map = toMap(convention.getLowCardinalityKeyValues(context));

        assertThat(map).containsEntry("http.request.method", "POST");
        assertThat(map).containsEntry("server.address", "api.example.com");
        assertThat(map).containsEntry("server.port", "8443");
        assertThat(map).containsEntry("http.route", "/v1/{id}");
        assertThat(map).containsEntry("http.response.status_code", "201");
        assertThat(map).containsEntry("http.status_code_family", "2xx");
    }

    @Test
    @DisplayName("High cardinality includes url.full")
    void highCardinalityEmitsUrlFull() {
        var request = mock(ClientHttpRequest.class);
        when(request.getURI()).thenReturn(URI.create("https://api.example.com/v1/items?q=1"));

        var context = new ClientRequestObservationContext(request);

        var map = toMap(convention.getHighCardinalityKeyValues(context));

        assertThat(map).containsEntry("url.full", "https://api.example.com/v1/items?q=1");
    }

    @Test
    @DisplayName("Low cardinality always emits consistent tag keys even when route/response are absent")
    void lowCardinalityAlwaysEmitsConsistentKeys() {
        var request = mock(ClientHttpRequest.class);
        when(request.getMethod()).thenReturn(HttpMethod.GET);
        when(request.getURI()).thenReturn(URI.create("https://api.example.com/v1/items"));

        var context = new ClientRequestObservationContext(request);

        var map = toMap(convention.getLowCardinalityKeyValues(context));

        var expectedKeys = Set.of(
                "http.request.method", "server.address", "server.port",
                "http.route", "http.response.status_code", "http.status_code_family",
                "exception.type", "outcome");
        assertThat(map.keySet()).containsAll(expectedKeys);

        assertThat(map).containsEntry("http.route", "none");
        assertThat(map).containsEntry("http.response.status_code", "none");
        assertThat(map).containsEntry("http.status_code_family", "none");
        assertThat(map).containsEntry("exception.type", "none");
    }

    @Test
    @DisplayName("Contextual name uses METHOD /route when uri template is set")
    void contextualNameUsesMethodAndRoute() {
        var request = mock(ClientHttpRequest.class);
        when(request.getMethod()).thenReturn(HttpMethod.GET);

        var context = new ClientRequestObservationContext(request);
        context.setUriTemplate("https://api.example.com/api/health");

        assertThat(convention.getContextualName(context)).isEqualTo("GET /api/health");
    }

    private static java.util.Map<String, String> toMap(KeyValues keyValues) {
        return keyValues.stream().collect(Collectors.toMap(KeyValue::getKey, KeyValue::getValue));
    }
}
