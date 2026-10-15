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

import com.nvidia.ess.encryption.persistence.models.EncryptionKeyByTimestampModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import jakarta.annotation.PostConstruct;
import java.security.SecureRandom;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Random;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;

@Configuration
@ConfigurationProperties("encryption")
@Data
@Slf4j
public class EncryptionProperties {

    private RolloutProperties rollout = new RolloutProperties();
    private RotationProperties rotation = new RotationProperties();
    private ReencryptionProperties reencryption = new ReencryptionProperties();
    private PromotionProperties promotion = new PromotionProperties();
    private String tableNameByKid = EncryptionKeyModel.TABLE;
    private String tableNameByTimestamp = EncryptionKeyByTimestampModel.TABLE;
    private String tableNameByKidAndEncryptedAt = EncryptionKeyV2Model.TABLE;
    private CacheProperties cache = new CacheProperties();
    private IntegrityChecksProperties integrityChecks = new IntegrityChecksProperties();
    // will not attempt re-encryption if MEK was rotated in the last 48 hours based on UUID v1 kid
    // Vault agent default secret lease is 24 hours. Spring property reload is 15 minutes.
    // Worst case scenario is
    // 1. Vault agent secret refresh
    // 2. Immediately after, MEK rotated
    // 3. (24 hours later) Vault agent secret refresh
    // 4. (15 minutes later) Spring refreshes properties from file
    // 5. Start alerting after 24 hours, give another 24 hours to address the alert
    // Thus rounding up to 48 hours to account for any additional delays
    private Duration mekRotationGracePeriod = Duration.ofHours(48);
    private ImmutableTableProperties immutableTable = new ImmutableTableProperties();
    private Duration nekAgeAlertingOffset = Duration.ofHours(48);

    @PostConstruct
    public void init() {
        if (!integrityChecks.isPopulationEnabled()) {
            log.warn("integrity check fields population disabled");
        }

        if (!integrityChecks.isValidationEnabled()) {
            log.warn("integrity check fields validation disabled");
        }
    }

    @Data
    public static class RolloutProperties {

        // by default, force encryption keys
        private boolean enabled = true;
        private List<String> allowList = new ArrayList<>();

        private boolean useAllowList = false;
        // by default, backwards compatible
        private boolean useDefaultKey = true;
    }

    @Data
    @NoArgsConstructor
    @AllArgsConstructor
    @Builder
    public static class RotationProperties {
        private ScheduledProperties scheduled = new ScheduledProperties();
        private Duration compliancePeriod = Duration.ofDays(90);
        private boolean enabled = false;
        // ignores predicate
        private List<String> alwaysRotateList = new ArrayList<>();
    }


    @Data
    @NoArgsConstructor
    @AllArgsConstructor
    @Builder
    public static class ReencryptionProperties {
        private ScheduledProperties scheduled = new ScheduledProperties();
        private boolean enabled = false;
        // respects predicate
        private List<String> allowList = new ArrayList<>();

    }

    @Data
    public static class PromotionProperties {
        private boolean enabled = false;
        private List<String> allowList = new ArrayList<>();
        // TODO switch to dynamically generated payloads
        private List<String> validationPayloads = new ArrayList<>();
        private ScheduledProperties scheduled = new ScheduledProperties();
    }

    @Data
    @NoArgsConstructor
    @AllArgsConstructor
    @Builder(toBuilder = true)
    public static class ScheduledProperties {

        private boolean enabled = false;
        private static final Random RANDOM = new SecureRandom();
        // Compliance is 90 days, but start earlier
        private Duration period = Duration.ofDays(77);
        private int fetchSize = 100;
        private int backpressurePageCount = 4;
        private String cron =
                String.format("%d %d %d * * *", RANDOM.nextInt(60), RANDOM.nextInt(60),
                        RANDOM.nextInt(24));
    }

    @Data
    public static class CacheProperties {
        // refreshAfterWrite at half of eviction rate. No need to lower any further and increase DB pressure
        private BaseCacheProperties encryption = new BaseCacheProperties(Duration.ofMinutes(60), 256, Duration.ofMinutes(30));
        private BaseCacheProperties decryption = new BaseCacheProperties(Duration.ofMinutes(60), 1024, Duration.ofMinutes(30));

        @Data
        @AllArgsConstructor
        public static class BaseCacheProperties {
            private Duration ttl;
            private int maxSize;
            // Must be lower than ttl to be effective
            private Duration refreshAfterWrite;

        }
    }

    @Data
    public static class ImmutableTableProperties {
        private boolean nekv2WriteEnabled = false;
        private List<String> nekV2WriteAllowList = new ArrayList<>();

        private boolean nekv2ReadEnabled = false;
        // can be disabled once old table is retired
        private boolean nekv1FallbackReadEnabled = false;
    }

    @Data
    public static class IntegrityChecksProperties {

        // by default, population of integrity checks disabled
        private boolean populationEnabled = false;

        // by default, validation of integrity checks disabled
        private boolean validationEnabled = false;
    }
}
