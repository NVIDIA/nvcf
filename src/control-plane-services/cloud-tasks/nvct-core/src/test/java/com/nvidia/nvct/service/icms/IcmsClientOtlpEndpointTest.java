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

package com.nvidia.nvct.service.icms;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatIllegalArgumentException;

import java.net.URI;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.NullAndEmptySource;
import org.junit.jupiter.params.provider.ValueSource;

class IcmsClientOtlpEndpointTest {

    @ParameterizedTest
    @NullAndEmptySource
    @ValueSource(strings = {"  "})
    void unsetEndpointSendsNoOtlpEndpoint(String endpoint) {
        var tracingUrl = IcmsClient.parseTracingUrl(endpoint);

        assertThat(tracingUrl).isNull();
        assertThat(IcmsClient.otlpEndpointEnv(tracingUrl)).isEmpty();
    }

    @Test
    void configuredEndpointIsSent() {
        var tracingUrl = IcmsClient.parseTracingUrl(" https://otel-collector.example.invalid:4317 ");

        assertThat(tracingUrl).isEqualTo(URI.create("https://otel-collector.example.invalid:4317"));
        assertThat(IcmsClient.otlpEndpointEnv(tracingUrl))
                .singleElement()
                .extracting("first", "second")
                .containsExactly("OTEL_EXPORTER_OTLP_ENDPOINT",
                                 "https://otel-collector.example.invalid:4317");
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "http://otel-collector.example.invalid:4318",
            "http://otel_collector:4318",
            "http://otel-collector.example.invalid:",
            "https://otel-collector.example.invalid",
            "http://[::1]:4317",
            "http://[::1]"
    })
    void validEndpointIsAccepted(String endpoint) {
        assertThat(IcmsClient.parseTracingUrl(endpoint)).isEqualTo(URI.create(endpoint));
    }

    @ParameterizedTest
    @ValueSource(strings = {
            "http://bad host:4317",
            "not a uri",
            "otel-collector.example.invalid:4317",
            "grpc://otel-collector.example.invalid:4317",
            "http:otel-collector.example.invalid",
            "https:///v1/traces",
            "http://otel-collector.example.invalid:notaport",
            "http://otel_collector:notaport",
            "http://otel-collector.example.invalid:65536",
            "http://otel-collector.example.invalid:99999999999",
            "http://otel-collector.example.invalid:4317:4318",
            "http://otel_collector:4317:4318"
    })
    void malformedEndpointIsRejected(String endpoint) {
        assertThatIllegalArgumentException()
                .isThrownBy(() -> IcmsClient.parseTracingUrl(endpoint))
                .withMessageContaining("management.opentelemetry.tracing.export.otlp.endpoint")
                .withMessageContaining("'" + endpoint + "'");
    }
}
