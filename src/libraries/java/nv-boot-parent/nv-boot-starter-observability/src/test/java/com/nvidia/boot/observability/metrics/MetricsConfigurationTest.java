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

package com.nvidia.boot.observability.metrics;

import static org.assertj.core.api.Assertions.assertThat;

import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;
import org.springframework.boot.micrometer.metrics.autoconfigure.MeterRegistryCustomizer;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;

class MetricsConfigurationTest {

    @Test
    void addsCommonTagsToRegistry() {
        var runner = new ApplicationContextRunner()
                .withPropertyValues(
                        "management.metrics.tags.env=prod",
                        "management.metrics.tags.host_id=host-1",
                        "management.metrics.tags.host_dc=us-west-2")
                .withUserConfiguration(MetricsConfiguration.class);

        runner.run(context -> {
            var customizer = context.getBean(MeterRegistryCustomizer.class);
            var registry = new SimpleMeterRegistry();
            customizer.customize(registry);

            registry.counter("test.counter").increment();

            var counter = registry.find("test.counter").counter();
            assertThat(counter).isNotNull();
            assertThat(counter.getId().getTag("env")).isEqualTo("prod");
            assertThat(counter.getId().getTag("host_id")).isEqualTo("host-1");
            assertThat(counter.getId().getTag("host_dc")).isEqualTo("us-west-2");
        });
    }
}
