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
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import java.time.Duration;
import org.junit.jupiter.api.Test;
import org.springframework.boot.health.actuate.endpoint.HealthEndpoint;
import org.springframework.boot.health.contributor.Status;

class CachedHealthResponseServiceTest {

    @Test
    void cachesUntilTtlExpires() {
        HealthEndpoint healthEndpoint = mock(HealthEndpoint.class);
        when(healthEndpoint.health()).thenReturn(HealthDescriptors.withStatus(Status.UP));

        var service =
                new CachedHealthResponseService(healthEndpoint, HealthResponseCacheProperties.of(Duration.ofMinutes(1)));

        service.getHealth();
        service.getHealth();
        service.getHealth();

        verify(healthEndpoint, times(1)).health();
    }

    @Test
    void refreshesWhenTtlIsZero() {
        HealthEndpoint healthEndpoint = mock(HealthEndpoint.class);
        when(healthEndpoint.health()).thenReturn(HealthDescriptors.withStatus(Status.UP));

        var service = new CachedHealthResponseService(healthEndpoint, HealthResponseCacheProperties.of(Duration.ZERO));

        service.getHealth();
        service.getHealth();

        verify(healthEndpoint, times(2)).health();
    }

    @Test
    void defaultConfigurationMatchesDefaultTimeToLive() {
        assertThat(HealthResponseCacheProperties.ofDefaults().timeToLive())
                .isEqualTo(HealthResponseCacheProperties.DEFAULT_TIME_TO_LIVE);
    }

    @Test
    void rejectsNegativeTtl() {
        assertThatThrownBy(() -> HealthResponseCacheProperties.of(Duration.ofMillis(-1)))
                .isInstanceOf(IllegalArgumentException.class);
    }
}
