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
package com.nvidia.ess.encryption.config;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import io.micrometer.core.instrument.MeterRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.support.GenericApplicationContext;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
class RotationSchedulerConfigTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(RotationSchedulerConfig.class, EncryptionProperties.class,
                    CryptoProperties.class);

    @Test
    void applicationConfiguration_onRotationScheduleDisabled_hasNoServletKeyRotatorScheduledService() {
        contextRunner
                .withPropertyValues("encryption.rotation.scheduled.enabled=false")
                .withInitializer(ctx -> {
                    var context = (GenericApplicationContext) ctx;
                    context.registerBean(EncryptionMetricsRegistry.class,
                            () -> Mockito.mock(EncryptionMetricsRegistry.class));
                    context.registerBean(MeterRegistry.class,
                            () -> Mockito.mock(MeterRegistry.class));
                    context.registerBean(EncryptionKeyRotationService.class,
                            () -> Mockito.mock(EncryptionKeyRotationService.class));
                })
                .run(context -> assertThat(context)
                        .doesNotHaveBean("servletKeyRotatorScheduledService")
                        .doesNotHaveBean("reactiveKeyRotatorScheduledService"));
    }

    @Test
    void applicationConfiguration_onRotationScheduleEnabled_createsServletKeyRotatorScheduledService() {
        contextRunner
                .withPropertyValues("encryption.rotation.scheduled.enabled=true")
                .withInitializer(ctx -> {
                    var context = (GenericApplicationContext) ctx;
                    context.registerBean(EncryptionMetricsRegistry.class,
                            () -> Mockito.mock(EncryptionMetricsRegistry.class));
                    context.registerBean(MeterRegistry.class,
                            () -> Mockito.mock(MeterRegistry.class));
                    context.registerBean(CryptoPropertiesHolder.BEAN_NAME, RefreshScopedBeanHolder.class,
                            () -> Mockito.mock(CryptoPropertiesHolder.class));
                    context.registerBean(EncryptionKeyRotationService.class,
                            () -> Mockito.mock(EncryptionKeyRotationService.class));
                })
                .run(context -> assertThat(context).hasBean("servletKeyRotatorScheduledService"));
    }
}
