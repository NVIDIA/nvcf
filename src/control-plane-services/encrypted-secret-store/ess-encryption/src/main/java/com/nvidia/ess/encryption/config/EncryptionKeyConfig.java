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

import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.AllowListEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.CompatibleEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.DefaultKeyEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.persistence.models.naming.EncryptionKeyNamingStrategy;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.data.cassandra.core.mapping.CassandraMappingContext;

@Configuration
@Slf4j
public class EncryptionKeyConfig {

    @Bean
    public EncryptionKeyService encryptionKeyService(EncryptionProperties encryptionProperties) {
        if (!encryptionProperties.getRollout().isEnabled()) {
            log.info("Single encryption key will be used. "
                    + "Using Default implementation of EncryptionKeyService");
            return new DefaultKeyEncryptionKeyService();
        }

        if (encryptionProperties.getRollout().isUseAllowList()) {
            log.info("using Allow list implementation of EncryptionKeyService");
            return new AllowListEncryptionKeyService();
        }

        if (encryptionProperties.getRollout().isUseDefaultKey()) {
            log.info("using Backwards compatible implementation of EncryptionKeyService");
            return new CompatibleEncryptionKeyService();
        }

        log.info("using Base implementation of EncryptionKeyService");
        return new BaseEncryptionKeyService();
    }

    @Bean
    @ConditionalOnProperty(value = "encryption.rollout.enabled", havingValue = "true")
    public EncryptionKeyReencryptionService encryptionKeyReencryptionService(EncryptionProperties encryptionProperties) {
        return baseEncryptionKeyService(encryptionProperties, "EncryptionKeyReencryptionService");
    }

    @Bean
    @ConditionalOnProperty(value = "encryption.rollout.enabled", havingValue = "true")
    public EncryptionKeyRotationService encryptionKeyRotationService(
            EncryptionProperties encryptionProperties) {
        return baseEncryptionKeyService(encryptionProperties, "EncryptionKeyRotationService");
    }

    private BaseEncryptionKeyService baseEncryptionKeyService(
            EncryptionProperties encryptionProperties,
            String serviceName) {
        if (encryptionProperties.getRollout().isUseAllowList()) {
            log.info("using Allow list implementation of {}", serviceName);
            return new AllowListEncryptionKeyService();
        }

        if (encryptionProperties.getRollout().isUseDefaultKey()) {
            log.info("using Backwards compatible implementation of {}", serviceName);
            return new CompatibleEncryptionKeyService();
        }

        log.info("using Base implementation of {}", serviceName);
        return new BaseEncryptionKeyService();
    }

    // Above will end up creating 3 same beans in the context under different qualifier names,
    //  but should be fine as BaseEncryptionKeyService is stateless
    // As an alternative, I thought of unifying under an interface that extends both interfaces,
    //  but that would defeat the purpose of splitting
    // Additionally, might be able to make it work if BaseEncryptionKeyService is a singleton,
    //  but I am not 100% how that would behave in @RefreshScope thread local scenarios

    @Bean
    public CassandraMappingContext cassandraMapping(EncryptionProperties encryptionProperties) {
        CassandraMappingContext mappingContext = new CassandraMappingContext();
        mappingContext.setNamingStrategy(
                new EncryptionKeyNamingStrategy(encryptionProperties.getTableNameByKid(),
                        encryptionProperties.getTableNameByTimestamp()));
        return mappingContext;
    }
}
