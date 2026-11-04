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
package com.nvidia.icms.configuration.nats;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.health.autoconfigure.contributor.CompositeHealthContributorConfiguration;
import org.springframework.boot.health.contributor.AbstractHealthIndicator;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthContributor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration(proxyBeanMethods = false)
@ConditionalOnProperty(prefix = "icms.nats", name = "enabled", havingValue = "true")
public class NatsHealthConfiguration extends
        CompositeHealthContributorConfiguration<NatsHealthConfiguration.NatsHealthIndicator, FixedNatsPool> {

    NatsHealthConfiguration() {
        super(NatsHealthIndicator::new);
    }

    @RequiredArgsConstructor
    public static class NatsHealthIndicator extends AbstractHealthIndicator {

        private final FixedNatsPool connection;

        @Override
        protected void doHealthCheck(Health.Builder builder) {
            if (connection.healthy()) {
                builder.up();
            } else {
                builder.down();
            }
        }
    }

    @Bean
    HealthContributor natsHealthContributor(Map<String, FixedNatsPool> connections) {
        return createContributor(connections);
    }
}
