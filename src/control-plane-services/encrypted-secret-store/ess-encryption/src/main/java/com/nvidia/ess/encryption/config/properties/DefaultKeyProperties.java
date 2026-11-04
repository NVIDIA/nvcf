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
package com.nvidia.ess.encryption.config.properties;

import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.DefaultKeyProperties.DefaultKeyPropertyCondition;
import jakarta.annotation.PostConstruct;
import java.text.ParseException;
import java.util.Map;
import lombok.Getter;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.AnyNestedCondition;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Conditional;
import org.springframework.context.annotation.Configuration;

/**
 * A Spring ConfigurationProperties class to load default key
 */
@RefreshScope
@Configuration
@ConfigurationProperties("kv.defaultkey")
@Conditional(DefaultKeyPropertyCondition.class)
@Slf4j
public class DefaultKeyProperties extends AbstractCryptoProperties {
    @Setter
    private String defaultKey;

    @Setter
    private String allDefaultKeys;

    // Populate keys from the Vault for encryption/decryption.
    // When keys are rotated in the Vault, vault agent detects the change in
    // the keys and a refresh is triggered. With RefreshScope annotation, this
    // bean gets re-initialized and init PostConstruct function triggers
    // populateKeys to reload the latest keys.
    @PostConstruct
    public void init() throws ParseException {
        log.info("reloading default encryption key");

        try {
            this.parsedDefaultKey = parseKey(this.defaultKey);
            this.parsedAllDefaultKeys = parseAllKeys(this.allDefaultKeys);

        } catch (ParseException e) {
            log.error("Failed to populate default encryption/decryption keys", e);
            throw e;
        }
    }

    @Getter
    private Map<String, OctetSequenceKey> parsedAllDefaultKeys;
    @Getter
    private OctetSequenceKey parsedDefaultKey;


    static class DefaultKeyPropertyCondition extends AnyNestedCondition {

        DefaultKeyPropertyCondition() {
            super(ConfigurationPhase.PARSE_CONFIGURATION);
        }

        @ConditionalOnProperty(value = "encryption.rollout.useDefaultKey", havingValue = "true")
        static class UseDefaultKeyCondition {

        }

        @ConditionalOnProperty(value = "encryption.rollout.enabled", havingValue = "false")
        static class DisableRolloutCondition {

        }
    }
}
