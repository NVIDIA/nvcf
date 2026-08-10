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
package com.nvidia.ess.encryption.scheduled;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptedPastDurationPredicate;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.util.DurationFormatUtils;
import jakarta.annotation.PostConstruct;
import java.time.Duration;
import java.time.Instant;
import java.util.UUID;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.scheduling.annotation.Scheduled;
import reactor.core.publisher.Mono;

@Slf4j
public class KeyReencryptionScheduledService {

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyReencryptionService encryptionKeyReencryptionService;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @PostConstruct
    public void init() {
        log.info("encryption keys re-encryption schedule: {}",
                encryptionProperties.getReencryption().getScheduled().getCron());
    }


    // non-overlapping. In the worst case of overlapping cron schedules due to long processing,
    //  execution will be skipped
    // if more scheduled service are added,
    // 1. increase spring.task.scheduling.pool.size
    // 2. use a separate thread pool
    //
    // Should not normally return values, but returning for testability
    @Scheduled(cron = "#{encryptionProperties.reencryption.scheduled.cron}")
    public Mono<Integer> reencrypt() {
        // using Mono.defer() because Spring will obtain the publisher and schedule subscription to it periodically
        // Otherwise, any non-reactive code before the Mono publish will be executed once only on startup
        // https://docs.spring.io/spring-framework/reference/integration/scheduling.html#scheduling-annotation-support-scheduled-reactive
        return Mono.defer(() -> {
            log.info("Starting encryption key re-encryption");
            var start = Instant.now();

            if (shouldEarlySkipReencryption(start)) {
                log.warn(
                        "Re-encryption was triggered within {} of MEK rotation, skipping execution",
                        encryptionProperties.getMekRotationGracePeriod());
                return Mono.just(0);
            }

            return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                    new EncryptedPastDurationPredicate(
                            encryptionProperties.getReencryption().getScheduled().getPeriod(),
                            cryptoPropertiesHolder.get().getValidMek().getKeyID()))
                    .doOnSuccess(reencryptedKeysCount -> {
                        var end = Instant.now();
                        log.info("Re-encrypted {} encryption keys, took {}", reencryptedKeysCount,
                                DurationFormatUtils.formatDurationWords(
                                        Duration.between(start, end).toMillis()));
                    })
                    .doOnError(err -> {
                        var end = Instant.now();
                        encryptionMetricsRegistry.recordNekReencryptionError(
                                err.getClass().getName());
                        log.error("Failed to re-encrypt encryption keys, took {}",
                                DurationFormatUtils.formatDurationWords(
                                        Duration.between(start, end).toMillis()), err);
                    });
        });
    }


    private boolean shouldEarlySkipReencryption(Instant now) {
        String kid = cryptoPropertiesHolder.get().getActualParsedMasterKey().getKeyID();

        // kid was verified to be UUIDv1 already, not revalidating again
        Instant mekTimestamp = Instant.ofEpochMilli(Uuids.unixTimestamp((UUID.fromString(kid))));
        return Duration.between(mekTimestamp, now)
                .compareTo(encryptionProperties.getMekRotationGracePeriod()) < 0;
    }
}
