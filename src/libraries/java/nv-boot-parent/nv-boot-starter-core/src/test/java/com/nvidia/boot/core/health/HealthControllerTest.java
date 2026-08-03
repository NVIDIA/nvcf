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

package com.nvidia.boot.core.health;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import java.time.Duration;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.actuate.endpoint.HealthEndpoint;
import org.springframework.boot.health.contributor.Status;

class HealthControllerTest {

    private HealthEndpoint healthEndpoint;
    private HealthController controller;

    @BeforeEach
    void setUp() {
        healthEndpoint = mock(HealthEndpoint.class);
        // TTL 0: always refresh so tests mirror uncached HealthEndpoint behavior
        var cache = new CachedHealthResponseService(
                healthEndpoint, HealthResponseCacheProperties.of(Duration.ZERO));
        controller = new HealthController(cache);
    }

    @Test
    void getHealthReturnsOkWhenStatusUp() {
        when(healthEndpoint.health()).thenReturn(healthDescriptorWithStatus(Status.UP));

        var response = controller.getHealth();

        assertThat(response.getStatusCode().is2xxSuccessful()).isTrue();
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().getStatus().getCode()).isEqualTo("UP");
    }

    @Test
    void getHealthReturns503WhenStatusDown() {
        when(healthEndpoint.health()).thenReturn(healthDescriptorWithStatus(Status.DOWN));

        var response = controller.getHealth();

        assertThat(response.getStatusCode().value()).isEqualTo(503);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().getStatus().getCode()).isEqualTo("DOWN");
    }

    @Test
    void getHealthReturns500ForUnknownStatus() {
        when(healthEndpoint.health()).thenReturn(healthDescriptorWithStatus(Status.UNKNOWN));

        var response = controller.getHealth();

        assertThat(response.getStatusCode().value()).isEqualTo(500);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody().getStatus().getCode()).isEqualTo("UNKNOWN");
    }

    private static org.springframework.boot.health.actuate.endpoint.HealthDescriptor healthDescriptorWithStatus(
            Status status) {
        return HealthDescriptors.withStatus(status);
    }
}
