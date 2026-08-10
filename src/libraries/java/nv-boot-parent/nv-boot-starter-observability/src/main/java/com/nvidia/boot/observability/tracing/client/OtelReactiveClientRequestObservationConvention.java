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

import static com.nvidia.boot.observability.tracing.client.OtelClientObservationConventionSupport.statusCodeFamily;

import io.micrometer.common.KeyValue;
import io.micrometer.common.KeyValues;
import java.net.URI;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.Optional;
import org.apache.commons.lang3.StringUtils;
import org.springframework.web.reactive.function.client.ClientRequestObservationContext;
import org.springframework.web.reactive.function.client.DefaultClientRequestObservationConvention;

/**
 * WebClient observation convention that emits OpenTelemetry semantic attribute names for outbound HTTP
 * client spans (e.g. {@code http.request.method}, {@code server.address}, {@code url.full}).
 */
public class OtelReactiveClientRequestObservationConvention
        extends DefaultClientRequestObservationConvention {

    private static final String HTTP_REQUEST_METHOD = "http.request.method";
    private static final String HTTP_RESPONSE_STATUS_CODE = "http.response.status_code";
    private static final String HTTP_STATUS_CODE_FAMILY = "http.status_code_family";
    private static final String HTTP_ROUTE = "http.route";
    private static final String SERVER_ADDRESS = "server.address";
    private static final String SERVER_PORT = "server.port";
    private static final String URL_FULL = "url.full";
    private static final String EXCEPTION_TYPE = "exception.type";

    public OtelReactiveClientRequestObservationConvention() {
        super();
    }

    @Override
    public String getContextualName(ClientRequestObservationContext context) {
        var request = context.getRequest();
        if (request == null) {
            return super.getContextualName(context);
        }
        var method = request.method().name();
        var template = context.getUriTemplate();
        if (StringUtils.isNotBlank(template)) {
            return method + " " + OtelClientObservationConventionSupport
                                        .extractPathFromUriTemplate(template);
        }
        return method;
    }

    @Override
    public KeyValues getLowCardinalityKeyValues(ClientRequestObservationContext context) {
        var list = new ArrayList<KeyValue>();

        var request = context.getRequest();
        if (request != null) {
            var uri = Optional.ofNullable(request.url());
            list.add(KeyValue.of(SERVER_ADDRESS, uri.map(URI::getHost).filter(StringUtils::isNotBlank).orElse(KeyValue.NONE_VALUE)));
            list.add(KeyValue.of(SERVER_PORT, uri.map(URI::getPort).filter(p -> p >= 0).map(String::valueOf).orElse(KeyValue.NONE_VALUE)));
            list.add(KeyValue.of(HTTP_REQUEST_METHOD, request.method().name()));
        } else {
            list.add(KeyValue.of(SERVER_ADDRESS, KeyValue.NONE_VALUE));
            list.add(KeyValue.of(SERVER_PORT, KeyValue.NONE_VALUE));
            list.add(KeyValue.of(HTTP_REQUEST_METHOD, KeyValue.NONE_VALUE));
        }

        var template = context.getUriTemplate();
        if (StringUtils.isNotBlank(template)) {
            list.add(KeyValue.of(HTTP_ROUTE, OtelClientObservationConventionSupport
                                                    .extractPathFromUriTemplate(template)));
        } else {
            list.add(KeyValue.of(HTTP_ROUTE, KeyValue.NONE_VALUE));
        }

        var response = context.getResponse();
        if (!context.isAborted() && response != null) {
            var statusCode = response.statusCode().value();
            list.add(KeyValue.of(HTTP_RESPONSE_STATUS_CODE, String.valueOf(statusCode)));
            list.add(KeyValue.of(HTTP_STATUS_CODE_FAMILY, statusCodeFamily(statusCode)));
        } else {
            list.add(KeyValue.of(HTTP_RESPONSE_STATUS_CODE, KeyValue.NONE_VALUE));
            list.add(KeyValue.of(HTTP_STATUS_CODE_FAMILY, KeyValue.NONE_VALUE));
        }

        var error = context.getError();
        if (error != null) {
            list.add(KeyValue.of(EXCEPTION_TYPE, error.getClass().getName()));
        } else {
            list.add(KeyValue.of(EXCEPTION_TYPE, KeyValue.NONE_VALUE));
        }

        list.add(super.outcome(context));

        list.sort(Comparator.comparing(KeyValue::getKey));
        return KeyValues.of(list);
    }

    @Override
    public KeyValues getHighCardinalityKeyValues(ClientRequestObservationContext context) {
        var request = context.getRequest();
        if (request == null) {
            return KeyValues.empty();
        }
        var uri = request.url();
        return KeyValues.of(KeyValue.of(URL_FULL, uri.toASCIIString()));
    }
}
