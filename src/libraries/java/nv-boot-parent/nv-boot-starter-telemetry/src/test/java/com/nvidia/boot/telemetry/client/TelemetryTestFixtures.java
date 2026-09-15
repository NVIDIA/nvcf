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

package com.nvidia.boot.telemetry.client;

import io.cloudevents.CloudEvent;
import io.cloudevents.core.builder.CloudEventBuilder;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;

/**
 * Shared test data for telemetry tests.
 */
public final class TelemetryTestFixtures {

    private TelemetryTestFixtures() {
    }

    public static TelemetryProperties telemetryProperties(String baseUrl, String tokenPath) {
        var normalized = baseUrl.replaceAll("/$", "");
        var tokenUri = tokenPath.startsWith("/")
                ? normalized + tokenPath
                : normalized + "/" + tokenPath;
        var oauth2 = TelemetryProperties.OAuth2Properties.builder()
                .tokenUri(tokenUri)
                .clientId("test-client-id")
                .clientSecret("test-client-secret")
                .scope("telemetry")
                .build();
        return TelemetryProperties.builder()
                .url(baseUrl)
                .pathPrefix("/api/v2/topic")
                .source("test-source")
                .oauth2(oauth2)
                .connectTimeout(Duration.ofSeconds(5))
                .readTimeout(Duration.ofSeconds(5))
                .build();
    }

    public static CloudEvent sampleCloudEvent() {
        var payload = "{\"k\":\"v\"}".getBytes(StandardCharsets.UTF_8);
        return CloudEventBuilder.v1()
                .withId("evt-1")
                .withType("com.nvidia.test.Event")
                .withSource(URI.create("urn:test:telemetry"))
                .withDataContentType("application/json")
                .withData(payload)
                .build();
    }

    public static List<CloudEvent> sampleCloudEvents() {
        return List.of(sampleCloudEvent());
    }
}
