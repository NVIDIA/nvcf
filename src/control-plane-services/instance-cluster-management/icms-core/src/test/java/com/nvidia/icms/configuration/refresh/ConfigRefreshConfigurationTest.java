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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import java.util.Map;
import java.util.Set;
import java.util.concurrent.atomic.AtomicReference;
import org.junit.jupiter.api.Test;
import org.mockito.ArgumentCaptor;
import org.springframework.boot.availability.ApplicationAvailability;
import org.springframework.boot.availability.ApplicationAvailabilityBean;
import org.springframework.boot.availability.AvailabilityChangeEvent;
import org.springframework.boot.availability.ReadinessState;
import org.springframework.cloud.context.refresh.ContextRefresher;
import org.springframework.cloud.kubernetes.commons.config.reload.ConfigurationUpdateStrategy;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.context.annotation.AnnotationConfigApplicationContext;
import org.springframework.core.env.MapPropertySource;

class ConfigRefreshConfigurationTest {

    private final ConfigRefreshConfiguration configuration =
            new ConfigRefreshConfiguration();

    private final ContextRefresher contextRefresher = mock(ContextRefresher.class);

    private final ApplicationAvailability availability = mock(ApplicationAvailability.class);

    private final ApplicationEventPublisher eventPublisher =
            mock(ApplicationEventPublisher.class);

    @Test
    void failedRefreshRefusesTrafficWithoutStoppingTheUpdateStrategy() {
        when(contextRefresher.refresh()).thenThrow(new IllegalStateException("rebind failed"));
        var strategy = configuration.icmsConfigurationUpdateStrategy(
                contextRefresher, availability, eventPublisher);

        strategy.reloadProcedure().run();

        assertThat(strategy.name()).isEqualTo(ConfigRefreshConfiguration.STRATEGY_NAME);
        assertPublishedState(ReadinessState.REFUSING_TRAFFIC);
    }

    @Test
    void healthyRefreshLeavesAvailabilityUnchanged() {
        when(contextRefresher.refresh()).thenReturn(Set.of("icms.setting"));
        var strategy = configuration.icmsConfigurationUpdateStrategy(
                contextRefresher, availability, eventPublisher);

        strategy.reloadProcedure().run();

        verifyNoInteractions(eventPublisher);
    }

    @Test
    void successfulRefreshRecoversTrafficWhenStrategyOwnsReadinessRefusal() {
        when(contextRefresher.refresh()).thenReturn(Set.of());
        when(availability.getLastChangeEvent(ReadinessState.class))
                .thenReturn(new AvailabilityChangeEvent<>(
                        configuration, ReadinessState.REFUSING_TRAFFIC));
        var strategy = configuration.icmsConfigurationUpdateStrategy(
                contextRefresher, availability, eventPublisher);

        strategy.reloadProcedure().run();

        assertPublishedState(ReadinessState.ACCEPTING_TRAFFIC);
    }

    @Test
    void successfulRefreshDoesNotOverrideAnotherReadinessRefusal() {
        when(contextRefresher.refresh()).thenReturn(Set.of());
        when(availability.getLastChangeEvent(ReadinessState.class))
                .thenReturn(new AvailabilityChangeEvent<>(
                        new Object(), ReadinessState.REFUSING_TRAFFIC));
        var strategy = configuration.icmsConfigurationUpdateStrategy(
                contextRefresher, availability, eventPublisher);

        strategy.reloadProcedure().run();

        verifyNoInteractions(eventPublisher);
    }

    @Test
    void springContextMovesReadinessDownAndBackUpThroughTheCustomStrategy() {
        var refreshFailure = new AtomicReference<RuntimeException>();
        var refresher = mock(ContextRefresher.class);
        when(refresher.refresh()).thenAnswer(invocation -> {
            var failure = refreshFailure.get();
            if (failure != null) {
                throw failure;
            }
            return Set.of();
        });

        try (var context = new AnnotationConfigApplicationContext()) {
            context.registerBean(ContextRefresher.class, () -> refresher);
            context.registerBean(ApplicationAvailability.class, ApplicationAvailabilityBean::new);
            context.register(ConfigRefreshConfiguration.class);
            context.refresh();

            var strategy = context.getBean(ConfigurationUpdateStrategy.class);
            var applicationAvailability = context.getBean(ApplicationAvailability.class);

            refreshFailure.set(new IllegalStateException("rebind failed"));
            strategy.reloadProcedure().run();
            assertThat(applicationAvailability.getReadinessState())
                    .isEqualTo(ReadinessState.REFUSING_TRAFFIC);

            refreshFailure.set(null);
            strategy.reloadProcedure().run();
            assertThat(applicationAvailability.getReadinessState())
                    .isEqualTo(ReadinessState.ACCEPTING_TRAFFIC);
        }
    }

    @Test
    void customStrategyBacksOffWhenReloadIsNotConfiguredForRefresh() {
        try (var context = new AnnotationConfigApplicationContext()) {
            context.getEnvironment()
                    .getPropertySources()
                    .addFirst(new MapPropertySource(
                            "test", Map.of("spring.cloud.kubernetes.reload.strategy", "shutdown")));
            context.register(ConfigRefreshConfiguration.class);
            context.refresh();

            assertThat(context.getBeansOfType(ConfigurationUpdateStrategy.class)).isEmpty();
        }
    }

    private void assertPublishedState(ReadinessState expected) {
        var eventCaptor = ArgumentCaptor.forClass(AvailabilityChangeEvent.class);
        verify(eventPublisher).publishEvent(eventCaptor.capture());
        var event = eventCaptor.getValue();
        assertThat(event.getSource()).isSameAs(configuration);
        assertThat(event.getState()).isEqualTo(expected);
    }
}
