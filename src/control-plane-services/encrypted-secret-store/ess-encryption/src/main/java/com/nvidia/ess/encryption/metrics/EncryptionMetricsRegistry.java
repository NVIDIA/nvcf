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
package com.nvidia.ess.encryption.metrics;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.google.common.annotations.VisibleForTesting;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import io.micrometer.common.KeyValue;
import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.MeterRegistry;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.apache.commons.lang3.tuple.Pair;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

/**
 * Observability flow could be the following for error state metrics: 1. A certain "error"
 * metric goes past allowable threshold (alert or not) 2. In logs, a corresponding message regex
 * should be usable to correlate to the metric 3. In tracing. a corresponding span attribute should
 * be usable to correlate to the traces
 * <p></p>
 *
 * Ideally, there should be a table in a runbook or some centralized document that allows to go from
 * any of the 3 signals (metric, log, span/trace) to figure out how to observe it in the other system
 */
@Component
@Slf4j
public class EncryptionMetricsRegistry {

    @Setter(onMethod_ = {@Autowired})
    private MeterRegistry meterRegistry;

    @Setter(onMethod_ = {@Autowired})
    private EncryptionProperties encryptionProperties;

    public static final String NAMESPACE_TAG = "ek_namespace";

    private static final String REASON_TAG = "reason";
    private static final String ERROR_KEY_TAG = "error_key";
    private static final String KID_TAG = "kid";
    private static final String OPERATION_SUCCESS_TAG = "success";

    public static final int DEFAULT_DAYS_OFFSET = 2;
    public static final int KID_VISIBLE_CHARACTERS = 3;

    private Gauge mekRotationDeltaGauge;
    private Gauge previousMekNotUuidV1Gauge;

    private static final String NEK_ROTATION_AGE_METRIC_NAME = "nek_rotation_age_delta";
    private final Map<String, Pair<AtomicLong, Gauge>> namespaceToCreatedAtAndGaugeMap = new ConcurrentHashMap<>();


    public Gauge registerMekRotationDelta(OctetSequenceKey mek) {
        if (mekRotationDeltaGauge != null) {
            meterRegistry.remove(mekRotationDeltaGauge);
        }

        mekRotationDeltaGauge = Gauge.builder("rotation_mek_delta", mek, key -> {
                    Instant now = Instant.now();
                    String keyId = key.getKeyID();
                    try {
                        UUID keyIdUuid = UUID.fromString(keyId);
                        if (keyIdUuid.version() != 1) {
                            log.error("Unable to record MEK age. MEK {} is not a UUID.", keyId);
                            // offset right away to more than rotation period to trigger alert
                            return encryptionProperties.getRotation().getScheduled().getPeriod().plus(
                                    Duration.ofDays(DEFAULT_DAYS_OFFSET)).toMillis();
                        }
                        return now.toEpochMilli() - Uuids.unixTimestamp(keyIdUuid);
                    } catch (IllegalArgumentException _) {
                        // should not happen, but adding just in case
                        log.error("Unable to record MEK age. MEK {} is not a UUID v1.", keyId);
                        // offset right away to more than rotation period to trigger alert
                        return encryptionProperties.getRotation().getScheduled().getPeriod().plus(
                                Duration.ofDays(DEFAULT_DAYS_OFFSET)).toMillis();
                    }
                })
                .tag(KID_TAG, maskKid(mek.getKeyID()))
                .description("gauge of millis since MEK was rotated (by kid)")
                .register(meterRegistry);
        return mekRotationDeltaGauge;
    }

    public Gauge registerPreviousMekNotUuidV1Count(int count) {
        if (previousMekNotUuidV1Gauge != null) {
            meterRegistry.remove(previousMekNotUuidV1Gauge);
        }
        previousMekNotUuidV1Gauge = Gauge.builder("previous_mek_not_uuidv1", () -> count)
                .description("gauge of number of rotated MEKs that are not UUIDv1")
                .register(meterRegistry);
        return previousMekNotUuidV1Gauge;
    }

    public void recordNekRotationError(String namespace, String reason) {
        Counter.builder("nek_rotation_errors")
                .tag(NAMESPACE_TAG, namespace)
                .tag(REASON_TAG, reason)
                .description("count of NEK rotation errors (per namespace and reason)")
                .register(meterRegistry)
                .increment();
    }
    public void recordNekRotationError(String reason) {
        Counter.builder("nek_rotation_errors")
                .tag(NAMESPACE_TAG, KeyValue.NONE_VALUE)
                .tag(REASON_TAG, reason)
                .description("count of NEK rotation errors (per namespace and reason)")
                .register(meterRegistry)
                .increment();
    }

    // age of NEK will be renewed every time this is invoked
    public Gauge registerNekRotationAgeDelta(String namespace, UUID createdAt) {
        long createdAtTimestamp;
        if (createdAt.version() != 1) {
            log.error("Unexpected error: unable to record NEK age. NEK's createdAt {} is not a UUID v1.",
                    createdAt);
            createdAtTimestamp = Instant.now().minus(encryptionProperties.getRotation().getScheduled().getPeriod()).minus(
                    Duration.ofDays(DEFAULT_DAYS_OFFSET)).toEpochMilli();
        } else {
            createdAtTimestamp = Uuids.unixTimestamp(createdAt);
        }

        var pair = namespaceToCreatedAtAndGaugeMap
                .computeIfAbsent(namespace, ns -> {
                    AtomicLong newAtomicTimestamp = new AtomicLong(createdAtTimestamp);
                    Gauge gauge = Gauge.builder(NEK_ROTATION_AGE_METRIC_NAME, newAtomicTimestamp, value ->
                            Instant.now().toEpochMilli() - value.get())
                            .tag(NAMESPACE_TAG, namespace)
                            .description("gauge of millis since NEK was rotated (by namespace)")
                            .register(meterRegistry);

                    return Pair.of(newAtomicTimestamp, gauge);
                });
        AtomicLong atomicTimestamp = pair.getLeft();
        atomicTimestamp.set(createdAtTimestamp);

        return pair.getRight();
    }

    public void removeNekRotationAgeDelta(String namespace) {
        namespaceToCreatedAtAndGaugeMap.computeIfPresent(namespace, (ns, pair) -> {
            meterRegistry.remove(pair.getRight());
            return null;
        });
    }

    public void removeNekRotationAgeDelta() {
        // keySet() is weakly consistent, so no concurrent modification exceptions
        for (String namespace: namespaceToCreatedAtAndGaugeMap.keySet()) {
            removeNekRotationAgeDelta(namespace);
        }
    }

    public void recordNekRotationAgeWarning(String namespace) {
        Counter.builder("nek_rotation_age_warnings")
                .tag(NAMESPACE_TAG, namespace)
                .description("count of fetched NEKs that should have been rotated already, but emitted way ahead of compliance schedule (per namespace)")
                .register(meterRegistry)
                .increment();
    }

    public void recordNekRotationAgeCritical(String namespace) {
        Counter.builder("nek_rotation_age_criticals")
                .tag(NAMESPACE_TAG, namespace)
                .description("count of fetched NEKs that need to be rotated within 1-2 days, meaning that nek_rotation_age_warnings_total was not acted upon (per namespace)")
                .register(meterRegistry)
                .increment();
    }


    @VisibleForTesting
    Map<String, Pair<AtomicLong, Gauge>> getNamespaceToCreatedAtAndGaugeMap() {
        return this.namespaceToCreatedAtAndGaugeMap;
    }

    public void recordNekPromotionError(String namespace, String kid, String reason) {
        Counter.builder("nek_promotion_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of NEK promotion errors (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordNekPromotionError(String reason) {
        Counter.builder("nek_promotion_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, KeyValue.NONE_VALUE)
                .tag(KID_TAG, KeyValue.NONE_VALUE)
                .description("count of NEK promotion errors (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }

    // potential high cardinality is okay since this should not happen often
    public void recordNekReencryptionError(String namespace, String kid, String reason) {
        Counter.builder("nek_reencryption_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of NEK reencryption errors (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordNekReencryptionError(String reason) {
        Counter.builder("nek_reencryption_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, KeyValue.NONE_VALUE)
                .tag(KID_TAG, KeyValue.NONE_VALUE)
                .description("count of NEK reencryption errors (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }


    // potential high cardinality is okay since this should not happen often
    public void recordNekValidationError(String namespace, String kid, KeyFetchErrorCode errorCode) {
        Counter.builder("nek_validation_errors")
                .tag(ERROR_KEY_TAG, errorCode.name())
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of NEK validation errors (per namespace, kid and validation error key)")
                .register(meterRegistry)
                .increment();
    }

    // exists because a NEKv1 failsafe might succeed
    // once NEKv1 is removed, will merge into recordGetEncryptionNekError
    public void recordGetEncryptionNekV2Error(String namespace, String reason) {
        Counter.builder("nek_v2_encryption_get_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .description("count of errors when reading from NEKv2 for Encryption operations (per namespace and reason)")
                .register(meterRegistry)
                .increment();
    }

    // exists because a NEKv1 failsafe might succeed
    // once NEKv1 is removed, will merge into recordGetEncryptionNekError
    public void recordGetDecryptionNekV2Error(String namespace, String kid, String reason) {
        Counter.builder("nek_v2_decryption_get_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of errors when reading from NEKv2 for Decryption operations (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }

    // tracking usage of NEKv1, don't need to track error since that will come in recordGetEncryptionNekError
    // will be removed once NEKv1 is removed
    public void recordGetEncryptionNekV1(String namespace, Optional<String> kidIfSuccessful) {
        Counter.builder("nek_v1_encryption_gets")
                .tag(NAMESPACE_TAG, namespace)
                .tag(OPERATION_SUCCESS_TAG, String.valueOf(kidIfSuccessful.isPresent()))
                .description("count of NEKv1 reads for Encryption operations (per namespace and isSuccess)")
                .register(meterRegistry)
                .increment();

        if (kidIfSuccessful.isPresent()) {
            Counter.builder("nek_v1_encryption_gets_by_kid")
                    .tag(NAMESPACE_TAG, namespace)
                    .tag(KID_TAG, maskKid(kidIfSuccessful.get()))
                    .description("count of successful NEKv1 reads for Encryption operations (per namespace and KID)")
                    .register(meterRegistry)
                    .increment();
        }
    }

    // tracking usage of NEKv1, don't need to track error since that will come in recordGetDecryptionNekError
    // will be removed once NEKv1 is removed
    public void recordGetDecryptionNekV1(String namespace, String kid, boolean isSuccessful) {
        Counter.builder("nek_v1_decryption_gets")
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .tag(OPERATION_SUCCESS_TAG, String.valueOf(isSuccessful))
                .description("count of NEKv1 reads for Decryption operations (per namespace, kid and isSuccess)")
                .register(meterRegistry)
                .increment();
    }

    // tracking usage of NEKv2 during rollout
    // will be removed once NEKv1 is removed
    public void recordGetEncryptionNekV2(String namespace, String kid) {
        Counter.builder("nek_v2_encryption_gets")
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of NEKv2 reads for Encryption operations (per namespace and kid)")
                .register(meterRegistry)
                .increment();
    }

    // tracking usage of NEKv2 during rollout
    // will be removed once NEKv1 is removed
    public void recordGetDecryptionNekV2(String namespace, String kid) {
        Counter.builder("nek_v2_decryption_gets")
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of NEKv2 reads for Decryption operations (per namespace and kid)")
                .register(meterRegistry)
                .increment();
    }

    public void recordGetEncryptionNekError(String namespace, String reason) {
        Counter.builder("nek_encryption_get_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .description("count of hard errors NEK reads for Encryption operations (per namespace and reason)")
                .register(meterRegistry)
                .increment();
    }

    public void recordGetDecryptionNekError(String namespace, String kid, String reason) {
        Counter.builder("nek_decryption_get_errors")
                .tag(REASON_TAG, reason)
                .tag(NAMESPACE_TAG, namespace)
                .tag(KID_TAG, maskKid(kid))
                .description("count of hard errors NEK reads for Decryption operations (per namespace, kid and reason)")
                .register(meterRegistry)
                .increment();
    }

    public static String maskKid(String kid) {
        if (StringUtils.isBlank(kid)) {
            return kid;
        }

        int length = kid.length();

        if (length <= KID_VISIBLE_CHARACTERS * 2) {
            log.error("kid is shorter than expected {}, fully masking kid", KID_VISIBLE_CHARACTERS * 2);
            return StringUtils.repeat('*', length);
        }

        String prefix = kid.substring(0, KID_VISIBLE_CHARACTERS);
        String middle = StringUtils.repeat("*", length - 2 * KID_VISIBLE_CHARACTERS);
        String suffix = kid.substring(length - KID_VISIBLE_CHARACTERS , length);

        return prefix + middle + suffix;
    }
}
