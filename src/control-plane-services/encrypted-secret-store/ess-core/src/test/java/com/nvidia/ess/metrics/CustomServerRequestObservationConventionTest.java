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
package com.nvidia.ess.metrics;

import static com.nvidia.ess.constants.Constants.UNKNOWN_NAMESPACE;
import static com.nvidia.ess.constants.Constants.X_ESS_NAMESPACE_HEADER;
import static com.nvidia.ess.controller.request.SecretQueryType.QUERY_TYPE_PARAM;
import static com.nvidia.ess.metrics.CustomMetricsRegistry.NAMESPACE_TAG;
import static com.nvidia.ess.metrics.CustomMetricsRegistry.QUERY_TYPE_TAG;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.ess.controller.BaseController;
import com.nvidia.ess.controller.request.SecretQueryType;
import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.springframework.http.HttpMethod;
import org.springframework.http.server.reactive.observation.ServerRequestObservationContext;
import org.springframework.mock.http.server.reactive.MockServerHttpRequest;
import org.springframework.mock.http.server.reactive.MockServerHttpResponse;

class CustomServerRequestObservationConventionTest {

    @Test
    void getLowCardinalityKeyValues_onSetNamespaceHeader_setsNamespaceTag() {
        String namespace = UUID.randomUUID().toString();
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());
        // Create a mock context
        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        // Verify the custom key value is added
        KeyValues keyValues = observationConvention.getLowCardinalityKeyValues(context);
        assertThat(keyValues).contains(KeyValue.of(NAMESPACE_TAG, namespace))
                .contains(KeyValue.of(QUERY_TYPE_TAG, KeyValue.NONE_VALUE));
    }


    @Test
    void getLowCardinalityKeyValues_onMissingNamespaceHeader_setsUnknownNamespaceTag() {
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getLowCardinalityKeyValues(context);
        assertThat(keyValues).contains(KeyValue.of(NAMESPACE_TAG, UNKNOWN_NAMESPACE))
                .contains(KeyValue.of(QUERY_TYPE_TAG, KeyValue.NONE_VALUE));
    }


    @Test
    void getLowCardinalityKeyValues_onSecretGet_setsQueryType() {
        String queryType = SecretQueryType.LIST_SECRETS.toString().toLowerCase();
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .queryParam(QUERY_TYPE_PARAM, queryType)
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        context.setPathPattern(BaseController.API_PATH + "/{entityType}/{entityId}/**");

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getLowCardinalityKeyValues(context);
        // uppercase
        assertThat(keyValues).contains(KeyValue.of(QUERY_TYPE_TAG, queryType.toUpperCase()));
    }

    @Test
    void getLowCardinalityKeyValues_onSecretGetAndInvalidQueryType_setsInvalidQueryType() {
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .queryParam(QUERY_TYPE_PARAM, UUID.randomUUID().toString())
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        context.setPathPattern(BaseController.API_PATH + "/{entityType}/{entityId}/**");

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getLowCardinalityKeyValues(context);
        // uppercase
        assertThat(keyValues).contains(KeyValue.of(QUERY_TYPE_TAG, "INVALID"));
    }


    @Test
    void getLowCardinalityKeyValues_onSecretGetAndEmptyParam_setsDefaultQueryType() {
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        context.setPathPattern(BaseController.API_PATH + "/{entityType}/{entityId}/**");

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getLowCardinalityKeyValues(context);
        // uppercase
        assertThat(keyValues).contains(KeyValue.of(QUERY_TYPE_TAG, SecretQueryType.DEFAULT_VALUE));
    }


    @Test
    void getHighCardinalityKeyValues_onSetNamespaceHeader_setsNamespaceTag() {
        String namespace = UUID.randomUUID().toString();
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .header(X_ESS_NAMESPACE_HEADER, namespace)
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getHighCardinalityKeyValues(context);
        assertThat(keyValues).contains(KeyValue.of(NAMESPACE_TAG, namespace))
                .contains(KeyValue.of(QUERY_TYPE_TAG, KeyValue.NONE_VALUE));
    }


    @Test
    void getHighCardinalityKeyValues_onMissingNamespaceHeader_setsUnknownNamespaceTag() {
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getHighCardinalityKeyValues(context);
        assertThat(keyValues).contains(KeyValue.of(NAMESPACE_TAG, UNKNOWN_NAMESPACE))
                .contains(KeyValue.of(QUERY_TYPE_TAG, KeyValue.NONE_VALUE));
    }


    @Test
    void getHighCardinalityKeyValues_onSecretGet_setsQueryType() {
        String queryType = SecretQueryType.LIST_SECRETS.toString().toLowerCase();
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .queryParam(QUERY_TYPE_PARAM, queryType)
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        context.setPathPattern(BaseController.API_PATH + "/{entityType}/{entityId}/**");

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getHighCardinalityKeyValues(context);
        // uppercase
        assertThat(keyValues).contains(KeyValue.of(QUERY_TYPE_TAG, queryType.toUpperCase()));
    }


    @Test
    void getHighCardinalityKeyValues_onSecretGetAndInvalidQueryType_setsUnknownQueryType() {
        MockServerHttpRequest request = MockServerHttpRequest.method(HttpMethod.GET, "/some-endpoint")
                .queryParam(QUERY_TYPE_PARAM, UUID.randomUUID().toString())
                .build();

        MockServerHttpResponse response = new MockServerHttpResponse();
        ServerRequestObservationContext context = new ServerRequestObservationContext(request, response,
                Map.of());

        context.setPathPattern(BaseController.API_PATH + "/{entityType}/{entityId}/**");

        CustomServerRequestObservationConvention observationConvention = new CustomServerRequestObservationConvention();


        KeyValues keyValues = observationConvention.getHighCardinalityKeyValues(context);
        // uppercase
        assertThat(keyValues).contains(KeyValue.of(QUERY_TYPE_TAG, "INVALID"));
    }

}
