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

import static org.assertj.core.api.Assertions.assertThatThrownBy;

import tools.jackson.databind.json.JsonMapper;
import java.util.Collections;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.reactive.function.client.WebClient;

/**
 * Unit tests for {@link TelemetryClient} validation (no HTTP).
 */
@ExtendWith(MockitoExtension.class)
class TelemetryClientTest {

    @Mock
    private WebClient webClient;

    private final JsonMapper jsonMapper = JsonMapper.builder().build();

    @Test
    void sendRejectsEmptyCloudEvents() {
        var props = TelemetryTestFixtures.telemetryProperties("http://localhost", "/oauth/token");
        var client = new TelemetryClient(props, jsonMapper, webClient);

        assertThatThrownBy(() -> client.send("topic", Collections.emptyList()))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("CloudEvents");
    }

    @Test
    void sendRejectsBlankResourceName() {
        var props = TelemetryTestFixtures.telemetryProperties("http://localhost", "/oauth/token");
        var client = new TelemetryClient(props, jsonMapper, webClient);

        assertThatThrownBy(() -> client.send("  ", TelemetryTestFixtures.sampleCloudEvents()))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("Resource Name");
    }

    @Test
    void sendAsyncWrapsValidationFailure() {
        var props = TelemetryTestFixtures.telemetryProperties("http://localhost", "/oauth/token");
        var client = new TelemetryClient(props, jsonMapper, webClient);

        assertThatThrownBy(() -> client.sendAsync("r", List.of()).join())
                .hasCauseInstanceOf(IllegalStateException.class);
    }
}
