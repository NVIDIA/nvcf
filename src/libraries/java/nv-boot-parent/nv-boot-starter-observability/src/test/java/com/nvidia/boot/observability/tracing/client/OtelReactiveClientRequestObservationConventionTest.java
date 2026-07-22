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
import org.springframework.web.reactive.function.client.ClientRequest;
import org.springframework.web.reactive.function.client.ClientRequestObservationContext;
import org.springframework.web.reactive.function.client.ClientResponse;

class OtelReactiveClientRequestObservationConventionTest {

    private final OtelReactiveClientRequestObservationConvention convention = new OtelReactiveClientRequestObservationConvention();

    @Test
    @DisplayName("Low cardinality includes http.request.method, server.address, http.route, http.response.status_code")
    void lowCardinalityEmitsOtelKeys() {
        var requestBuilder = ClientRequest.create(HttpMethod.PUT, URI.create("https://upstream.example.net:9000/v2/foo"));
        var context = new ClientRequestObservationContext(requestBuilder);
        context.setRequest(requestBuilder.build());
        context.setUriTemplate("https://upstream.example.net:9000/v2/{name}");

        var response = mock(ClientResponse.class);
        when(response.statusCode()).thenReturn(HttpStatus.NO_CONTENT);
        context.setResponse(response);

        var map = toMap(convention.getLowCardinalityKeyValues(context));

        assertThat(map).containsEntry("http.request.method", "PUT");
        assertThat(map).containsEntry("server.address", "upstream.example.net");
        assertThat(map).containsEntry("server.port", "9000");
        assertThat(map).containsEntry("http.route", "/v2/{name}");
        assertThat(map).containsEntry("http.response.status_code", "204");
        assertThat(map).containsEntry("http.status_code_family", "2xx");
    }

    @Test
    @DisplayName("High cardinality includes url.full")
    void highCardinalityEmitsUrlFull() {
        var requestBuilder = ClientRequest.create(HttpMethod.GET, URI.create("https://upstream.example.net/path?x=y"));
        var context = new ClientRequestObservationContext(requestBuilder);
        context.setRequest(requestBuilder.build());

        var map = toMap(convention.getHighCardinalityKeyValues(context));

        assertThat(map).containsEntry("url.full", "https://upstream.example.net/path?x=y");
    }

    @Test
    @DisplayName("Low cardinality always emits consistent tag keys even when route/response are absent")
    void lowCardinalityAlwaysEmitsConsistentKeys() {
        var requestBuilder = ClientRequest.create(HttpMethod.GET, URI.create("https://upstream.example.net/v2/foo"));
        var context = new ClientRequestObservationContext(requestBuilder);
        context.setRequest(requestBuilder.build());

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
        var requestBuilder = ClientRequest.create(HttpMethod.DELETE, URI.create("https://upstream.example.net/r"));
        var context = new ClientRequestObservationContext(requestBuilder);
        context.setRequest(requestBuilder.build());
        context.setUriTemplate("https://upstream.example.net/r/{id}");

        assertThat(convention.getContextualName(context)).isEqualTo("DELETE /r/{id}");
    }

    private static java.util.Map<String, String> toMap(KeyValues keyValues) {
        return keyValues.stream().collect(Collectors.toMap(KeyValue::getKey, KeyValue::getValue));
    }
}
