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

package com.nvidia.nvcf.icms.client;

import static org.assertj.core.api.Assertions.assertThat;

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
}
