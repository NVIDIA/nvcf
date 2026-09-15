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

import com.google.common.base.Enums;
import com.nvidia.boot.observability.tracing.server.OtelReactiveServerRequestObservationConvention;
import com.nvidia.ess.controller.BaseController;
import com.nvidia.ess.controller.request.SecretQueryType;
import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpMethod;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.http.server.reactive.observation.ServerRequestObservationContext;
import org.springframework.stereotype.Component;

/**
 * Ref: https://docs.spring.io/spring-framework/reference/integration/observability.html#observability.config.conventions
 * Enrich http_server_* metrics with 'ess_namespace' tag
 */
@Component
public class CustomServerRequestObservationConvention
        extends OtelReactiveServerRequestObservationConvention {

    @Override
    public KeyValues getLowCardinalityKeyValues(ServerRequestObservationContext context) {
        return super.getLowCardinalityKeyValues(context)
                .and(namespace(context), queryType(context));
    }

    @Override
    public KeyValues getHighCardinalityKeyValues(ServerRequestObservationContext context) {
        return super.getHighCardinalityKeyValues(context)
                .and(namespace(context), queryType(context));
    }

    private KeyValue namespace(ServerRequestObservationContext context) {
        ServerHttpRequest request = context.getCarrier();

        // Retrieve directly from header since no reactive context is available
        String namespace = request.getHeaders().getFirst(X_ESS_NAMESPACE_HEADER);
        if (StringUtils.isBlank(namespace)) {
            return KeyValue.of(NAMESPACE_TAG, UNKNOWN_NAMESPACE);
        }

        return KeyValue.of(NAMESPACE_TAG, namespace);
    }

    private boolean isSecretGet(ServerRequestObservationContext context) {
        HttpMethod method = context.getCarrier().getMethod();
        return (BaseController.API_PATH + "/{entityType}/{entityId}/**").equals(
                context.getPathPattern()) && HttpMethod.GET.equals(method);
    }

    private KeyValue queryType(ServerRequestObservationContext context) {
        if (!isSecretGet(context)) {
            // required, prometheus warns against registering meters with different set of tags
            return KeyValue.of(QUERY_TYPE_TAG, KeyValue.NONE_VALUE);
        }

        ServerHttpRequest request = context.getCarrier();
        String queryType = request.getQueryParams().getFirst(QUERY_TYPE_PARAM);
        if (StringUtils.isBlank(queryType)) {
            return KeyValue.of(QUERY_TYPE_TAG, SecretQueryType.DEFAULT_VALUE);
        } else if (!Enums.getIfPresent(SecretQueryType.class, queryType.toUpperCase()).isPresent()) {
            return KeyValue.of(QUERY_TYPE_TAG, "INVALID");
        }

        return KeyValue.of(QUERY_TYPE_TAG, queryType.toUpperCase());
    }
}
