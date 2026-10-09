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
package com.nvidia.icms.configuration.refresh;

import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.availability.ApplicationAvailability;
import org.springframework.boot.availability.AvailabilityChangeEvent;
import org.springframework.boot.availability.ReadinessState;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.cloud.kubernetes.commons.config.reload.ConfigurationUpdateStrategy;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Configures the Spring Cloud Kubernetes refresh strategy to reflect configuration validity in
 * Spring Boot readiness.
 *
 * <p>{@link RefreshScopeBeanValidator} eagerly re-creates refresh-scoped beans before the refresh
 * completes. If binding or bean construction fails, the refresh throws and this strategy publishes
 * {@link ReadinessState#REFUSING_TRAFFIC}. A later successful refresh restores
 * {@link ReadinessState#ACCEPTING_TRAFFIC} when this strategy owns the current readiness refusal.
 */
@Configuration(proxyBeanMethods = false)
@Slf4j
public class ConfigRefreshConfiguration {

    static final String STRATEGY_NAME = "refresh";

    @Bean
    @ConditionalOnProperty(
            prefix = "spring.cloud.kubernetes.reload",
            name = "strategy",
            havingValue = STRATEGY_NAME,
            matchIfMissing = true)
    ConfigurationUpdateStrategy icmsConfigurationUpdateStrategy(
            ContextRefresher contextRefresher,
            ApplicationAvailability availability,
            ApplicationEventPublisher eventPublisher) {
        return new ConfigurationUpdateStrategy(
                STRATEGY_NAME,
                () -> refresh(contextRefresher, availability, eventPublisher));
    }

    private void refresh(
            ContextRefresher contextRefresher,
            ApplicationAvailability availability,
            ApplicationEventPublisher eventPublisher) {
        try {
            contextRefresher.refresh();
        } catch (RuntimeException failure) {
            log.error(
                    "Configuration refresh failed; refusing traffic until a subsequent refresh "
                            + "succeeds",
                    failure);
            AvailabilityChangeEvent.publish(
                    eventPublisher, this, ReadinessState.REFUSING_TRAFFIC);
            return;
        }

        var lastReadinessEvent = availability.getLastChangeEvent(ReadinessState.class);
        if (lastReadinessEvent != null
                && lastReadinessEvent.getSource() == this
                && lastReadinessEvent.getState() == ReadinessState.REFUSING_TRAFFIC) {
            log.info("Configuration refresh succeeded; accepting traffic again");
            AvailabilityChangeEvent.publish(
                    eventPublisher, this, ReadinessState.ACCEPTING_TRAFFIC);
        }
    }
}
