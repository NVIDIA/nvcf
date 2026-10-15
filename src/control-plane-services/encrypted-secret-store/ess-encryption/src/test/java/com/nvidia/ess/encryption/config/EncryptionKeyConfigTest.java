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
import com.nvidia.ess.encryption.config.properties.DefaultKeyProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.MekService;
import com.nvidia.ess.encryption.crypto.key.AllowListEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.CompatibleEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.DefaultKeyEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationReactiveHelper;
import com.nvidia.ess.encryption.crypto.key.validation.OctetSequenceKeyValidator;
import com.nvidia.ess.encryption.integrity.IntegrityChecks;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2PartitionRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.boot.test.context.runner.ApplicationContextRunner;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.data.cassandra.core.ReactiveCassandraTemplate;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
class EncryptionKeyConfigTest {

    private final ApplicationContextRunner contextRunner = new ApplicationContextRunner()
            .withUserConfiguration(EncryptionKeyConfig.class, TrivialConfiguration.class, EncryptionProperties.class);

    @EnableConfigurationProperties(EncryptionProperties.class)
    @Configuration
    static class TrivialConfiguration {
        @Bean
        public DefaultKeyProperties defaultKeyProperties() {
            return Mockito.mock(DefaultKeyProperties.class);
        }

        @Bean
        public CryptoProperties cryptoProperties() {
            return Mockito.mock(CryptoProperties.class);
        }

        @Bean(CryptoPropertiesHolder.BEAN_NAME)
        public RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder() {
            return Mockito.mock(CryptoPropertiesHolder.class);
        }

        @Bean
        public CrudEncryptionKeyService crudEncryptionKeyService() {
            return Mockito.mock(CrudEncryptionKeyService.class);
        }

        @Bean
        public EncryptionKeyRepository encryptionKeyRepository() {
            return Mockito.mock(EncryptionKeyRepository.class);
        }

        @Bean
        public EncryptionKeyV2Repository encryptionKeyV2Repository() {
            return Mockito.mock(EncryptionKeyV2Repository.class);
        }

        @Bean
        public EncryptionKeyV2PartitionRepository encryptionKeyV2PartitionRepository() {
            return Mockito.mock(EncryptionKeyV2PartitionRepository.class);
        }

        @Bean
        public EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository() {
            return Mockito.mock(EncryptionKeyByTimestampRepository.class);
        }

        @Bean
        public EncryptionKeyCustomRepository encryptionKeyCustomRepository() {
            return Mockito.mock(EncryptionKeyCustomRepository.class);
        }

        @Bean
        public ReactiveCassandraTemplate reactiveCassandraTemplate() {
            return Mockito.mock(ReactiveCassandraTemplate.class);
        }

        @Bean
        public MeterRegistry meterRegistry() {
            return new SimpleMeterRegistry();
        }

        @Bean
        public EncryptionMetricsRegistry encryptionMetricsRegistry() {
            return Mockito.mock(EncryptionMetricsRegistry.class);
        }

        @Bean
        public IntegrityChecks integrityChecks() {
            return Mockito.mock(IntegrityChecks.class);
        }

        @Bean
        public MekService mekService() {
            return Mockito.mock(MekService.class);
        }

        @Bean
        public KeyValidationExecutor keyValidationExecutor() {
            return Mockito.mock(KeyValidationExecutor.class);
        }

        @Bean
        public OctetSequenceKeyValidator octetSequenceKeyValidator() {
            return Mockito.mock(OctetSequenceKeyValidator.class);
        }

        @Bean
        public KeyValidationReactiveHelper keyValidationReactiveHelper() {
            return Mockito.mock(KeyValidationReactiveHelper.class);
        }
    }

    @Test
    void applicationConfiguration_onRolloutDisabled_createsDefaultKeyEncryptionKeyService() {
        contextRunner
                .withPropertyValues("encryption.rollout.enabled=false")
                .run(context -> {
                    assertThat(context).hasBean("encryptionKeyService");
                    assertThat(context).doesNotHaveBean("encryptionKeyRotationService");
                    assertThat(context).doesNotHaveBean("encryptionKeyReencryptionService");
                    assertThat(context).hasSingleBean(DefaultKeyEncryptionKeyService.class);
                });
    }

    @Test
    void applicationConfiguration_onRolloutDisabled_createsAllowListEncryptionKeyService() {
        contextRunner
                .withPropertyValues("encryption.rollout.enabled=true")
                .withPropertyValues("encryption.rollout.useAllowList=true")
                .run(context -> {
                    assertThat(context).hasBean("encryptionKeyService");
                    assertThat(context).hasBean("encryptionKeyRotationService");
                    assertThat(context).hasBean("encryptionKeyReencryptionService");
                    assertThat(context).getBeans(AllowListEncryptionKeyService.class).size().isEqualTo(3);
                });
    }

    @Test
    void applicationConfiguration_onRolloutDisabled_createsCompatibleEncryptionKeyService() {
        contextRunner
                .withPropertyValues("encryption.rollout.enabled=true")
                .withPropertyValues("encryption.rollout.useAllowList=false")
                .withPropertyValues("encryption.rollout.useDefaultKey=true")
                .run(context -> {
                    assertThat(context).hasBean("encryptionKeyService");
                    assertThat(context).hasBean("encryptionKeyRotationService");
                    assertThat(context).hasBean("encryptionKeyReencryptionService");
                    assertThat(context).getBeans(CompatibleEncryptionKeyService.class).size().isEqualTo(3);
                });
    }

    @Test
    void applicationConfiguration_onRolloutDisabled_createsBaseEncryptionKeyService() {
        contextRunner
                .withPropertyValues("encryption.rollout.enabled=true")
                .withPropertyValues("encryption.rollout.useAllowList=false")
                .withPropertyValues("encryption.rollout.useDefaultKey=false")
                .run(context -> {
                    assertThat(context).hasBean("encryptionKeyService");
                    assertThat(context).hasBean("encryptionKeyRotationService");
                    assertThat(context).hasBean("encryptionKeyReencryptionService");
                    assertThat(context).getBeans(BaseEncryptionKeyService.class).size().isEqualTo(3);
                });
    }
}
