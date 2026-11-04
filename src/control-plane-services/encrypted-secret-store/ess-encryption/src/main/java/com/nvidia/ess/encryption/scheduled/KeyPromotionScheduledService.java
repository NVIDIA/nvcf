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

import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyPromotionService;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.util.DurationFormatUtils;
import jakarta.annotation.PostConstruct;
import java.time.Duration;
import java.time.Instant;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.Pair;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.scheduling.annotation.Scheduled;
import reactor.core.publisher.Mono;

@Slf4j
public class KeyPromotionScheduledService {

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionKeyPromotionService encryptionKeyPromotionService;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @PostConstruct
    public void init() {
        log.info("encryption keys promotion schedule: {}",
                encryptionProperties.getPromotion().getScheduled().getCron());
    }

    @Scheduled(cron = "#{encryptionProperties.promotion.scheduled.cron}")
    public Mono<Pair<Integer, Integer>> promote() {
        // using Mono.defer() because Spring will obtain the publisher and schedule subscription to it periodically
        // Otherwise, any non-reactive code before the Mono publish will be executed once only on startup
        // https://docs.spring.io/spring-framework/reference/integration/scheduling.html#scheduling-annotation-support-scheduled-reactive
        return Mono.defer(() -> {
            log.info("Starting encryption key promotion");
            var start = Instant.now();

            return encryptionKeyPromotionService.promoteAllEncryptionKeys()
                    .doOnSuccess(promotionResult -> {
                        var end = Instant.now();
                        log.info("Promotion results: promoted {} encryption keys, failed to promote {} encryption keys, took {}", promotionResult.getLeft(), promotionResult.getRight(),
                                DurationFormatUtils.formatDurationWords(
                                        Duration.between(start, end).toMillis()));
                    })
                    .doOnError(err -> {
                        var end = Instant.now();
                        encryptionMetricsRegistry.recordNekPromotionError(
                                err.getClass().getName());
                        log.error("Hard failure attempting to promote encryption keys, took {}",
                                DurationFormatUtils.formatDurationWords(
                                        Duration.between(start, end).toMillis()), err);
                    });
        });
    }
}
