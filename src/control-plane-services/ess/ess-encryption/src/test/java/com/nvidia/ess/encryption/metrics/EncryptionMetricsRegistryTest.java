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

import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.DEFAULT_DAYS_OFFSET;
import static org.assertj.core.api.Assertions.assertThat;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import io.micrometer.core.instrument.Gauge;
import io.micrometer.core.instrument.Measurement;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import java.time.Instant;
import java.util.Optional;
import java.util.Random;
import java.util.UUID;
import org.assertj.core.data.Offset;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class EncryptionMetricsRegistryTest {

    private final MeterRegistry meterRegistry = new SimpleMeterRegistry();

    private final EncryptionProperties encryptionProperties = new EncryptionProperties();

    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @BeforeEach
    void init() {
        encryptionMetricsRegistry = new EncryptionMetricsRegistry();
        encryptionMetricsRegistry.setMeterRegistry(meterRegistry);
        encryptionMetricsRegistry.setEncryptionProperties(encryptionProperties);
    }


    @Test
    void recordNekRotationErrorPerNamespace() {
        encryptionMetricsRegistry.recordNekRotationError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString());
    }

    @Test
    void recordNekRotationErrorGlobal() {
        encryptionMetricsRegistry.recordNekRotationError(UUID.randomUUID().toString());
    }


    @Test
    void recordNekReencryptionErrorPerNek() {
        encryptionMetricsRegistry.recordNekReencryptionError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), UUID.randomUUID().toString());
    }

    @Test
    void recordNekReencryptionErrorGlobal() {
        encryptionMetricsRegistry.recordNekReencryptionError(UUID.randomUUID().toString());
    }



    @Test
    void registerMekRotationDelta_onRandomKidMek_returnsRotationPeriodPlus2Days() {
        Random random = new Random();
        int period = random.nextInt(1, 77);
        encryptionProperties.getRotation().getScheduled().setPeriod(Duration.ofDays(period));
        Gauge gauge = encryptionMetricsRegistry.registerMekRotationDelta(CryptoTestUtils.generateEncryptionKey());
        Measurement measurement = gauge.measure().iterator().next();

        Assertions.assertEquals(Duration.ofDays(period).plus(Duration.ofDays(DEFAULT_DAYS_OFFSET)).toMillis(), measurement.getValue());
    }


    @Test
    void registerMekRotationDelta_onUuidv4KidMek_returnsRotationPeriodPlus2Days() {
        Random random = new Random();
        int period = random.nextInt(1, 77);
        encryptionProperties.getRotation().getScheduled().setPeriod(Duration.ofDays(period));
        Gauge gauge = encryptionMetricsRegistry.registerMekRotationDelta(
                new OctetSequenceKey.Builder(CryptoTestUtils.generateEncryptionKey()).keyID(UUID.randomUUID().toString())
                        .build());
        Measurement measurement = gauge.measure().iterator().next();

        Assertions.assertEquals(Duration.ofDays(period).plus(Duration.ofDays(DEFAULT_DAYS_OFFSET)).toMillis(), measurement.getValue());
    }


    @Test
    void registerMekRotationDelta_onUuidv1Mek_returns() {
        Gauge gauge = encryptionMetricsRegistry.registerMekRotationDelta(
                new OctetSequenceKey.Builder(CryptoTestUtils.generateEncryptionKey()).keyID(
                                Uuids.timeBased().toString())
                        .build());
        Measurement measurement = gauge.measure().iterator().next();

        Assertions.assertTrue(Duration.ofMinutes(5).toMillis() > measurement.getValue());
    }


    @Test
    void registerMekRotationDelta_onMultipleCalls_removesPreviousCalls() {
        Gauge gauge1 = encryptionMetricsRegistry.registerMekRotationDelta(CryptoTestUtils.generateEncryptionKey());
        Gauge gauge2 = encryptionMetricsRegistry.registerMekRotationDelta(CryptoTestUtils.generateEncryptionKey());

        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge2));
        Assertions.assertFalse(meterRegistry.getMeters().contains(gauge1));

        // do reference check
        Assertions.assertSame(meterRegistry.getMeters().get(0), gauge2);
        Assertions.assertNotSame(meterRegistry.getMeters().get(0), gauge1);
    }

    @Test
    void registerPreviousMekNotUuidV1Count_returnsCount() {
        Gauge gauge = encryptionMetricsRegistry.registerPreviousMekNotUuidV1Count(10);

        Measurement measurement = gauge.measure().iterator().next();

        Assertions.assertEquals(10, measurement.getValue());
    }

    @Test
    void registerPreviousMekNotUuidV1Count_onMultipleCalls_removesPreviousCalls() {
        Gauge gauge1 = encryptionMetricsRegistry.registerPreviousMekNotUuidV1Count(10);
        Gauge gauge2 = encryptionMetricsRegistry.registerPreviousMekNotUuidV1Count(100);

        // do reference check
        Assertions.assertSame(meterRegistry.getMeters().get(0), gauge2);
        Assertions.assertNotSame(meterRegistry.getMeters().get(0), gauge1);
    }

    @Test
    void recordNekPromotionErrorPerNek() {
        encryptionMetricsRegistry.recordNekPromotionError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), UUID.randomUUID().toString());
    }

    @Test
    void recordNekPromotionErrorGlobal() {
        encryptionMetricsRegistry.recordNekPromotionError(UUID.randomUUID().toString());
    }

    @Test
    void recordNekValidationError() {
        encryptionMetricsRegistry.recordNekValidationError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), KeyFetchErrorCode.INTERNAL_KEY_FETCH_EXECUTION_ERROR);
    }

    @Test
    void recordGetEncryptionNekV2Error() {
        encryptionMetricsRegistry.recordGetEncryptionNekV2Error(UUID.randomUUID().toString(),
                UUID.randomUUID().toString());
    }

    @Test
    void recordGetDecryptionNekV2Error() {
        encryptionMetricsRegistry.recordGetDecryptionNekV2Error(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), UUID.randomUUID().toString());
    }

    @Test
    void recordGetEncryptionNekV1() {
        encryptionMetricsRegistry.recordGetEncryptionNekV1(UUID.randomUUID().toString(),
                Optional.of(UUID.randomUUID().toString()));
        encryptionMetricsRegistry.recordGetEncryptionNekV1(UUID.randomUUID().toString(),
                Optional.empty());   
    }

    @Test
    void recordGetDecryptionNekV1() {
        encryptionMetricsRegistry.recordGetDecryptionNekV1(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), true);
    }

    @Test
    void recordGetEncryptionNekError() {
        encryptionMetricsRegistry.recordGetEncryptionNekError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString());
    }

    @Test
    void recordGetDecryptionNekError() {
        encryptionMetricsRegistry.recordGetDecryptionNekError(UUID.randomUUID().toString(),
                UUID.randomUUID().toString(), UUID.randomUUID().toString());
    }

    @Test
    void registerNekRotationAgeDelta_onUuidv4_returnsRotationPeriodPlus2Days() {
        Random random = new Random();
        int period = random.nextInt(1, 77);
        encryptionProperties.getRotation().getScheduled().setPeriod(Duration.ofDays(period));
        Gauge gauge = encryptionMetricsRegistry.registerNekRotationAgeDelta(UUID.randomUUID().toString(), UUID.randomUUID());
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge));

        double measurement = gauge.measure().iterator().next().getValue();

        Duration offset = Duration.ofDays(period + DEFAULT_DAYS_OFFSET);

        assertThat(measurement).isCloseTo(
                (double) offset.toMillis(), Offset.offset((double) Duration.ofMinutes(1).toMillis()));
    }

    @Test
    void registerNekRotationAgeDelta_onUuidv1_returnsImmediateAge() {
        Gauge gauge = encryptionMetricsRegistry.registerNekRotationAgeDelta(UUID.randomUUID().toString(), Uuids.timeBased());
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge));

        double measurement = gauge.measure().iterator().next().getValue();

        assertThat(measurement).isCloseTo(
                0.0, Offset.offset((double) Duration.ofMinutes(1).toMillis()));
    }


    @Test
    void registerNekRotationAgeDelta_onDifferentNamespaces_createsMultipleGauges() {
        Instant now = Instant.now();
        Duration offset10Min = Duration.ofMinutes(10);
        Duration offset1Hour = Duration.ofHours(1);

        Gauge gauge1 = encryptionMetricsRegistry.registerNekRotationAgeDelta(UUID.randomUUID().toString(), Uuids.startOf(now.minus(offset10Min).toEpochMilli()));
        Gauge gauge2 = encryptionMetricsRegistry.registerNekRotationAgeDelta(UUID.randomUUID().toString(), Uuids.startOf(now.minus(offset1Hour).toEpochMilli()));

        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge2));
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge1));
        Assertions.assertNotEquals(gauge1, gauge2);

        double measurement1 = gauge1.measure().iterator().next().getValue();
        double measurement2 = gauge2.measure().iterator().next().getValue();

        assertThat(measurement1).isCloseTo(
                (double) offset10Min.toMillis(), Offset.offset((double) Duration.ofMinutes(1).toMillis()));

        assertThat(measurement2).isCloseTo(
                (double) offset1Hour.toMillis(), Offset.offset((double) Duration.ofMinutes(1).toMillis()));
    }


    @Test
    void registerNekRotationAgeDelta_onMultipleCalls_updatesGauge() {
        Instant now = Instant.now();
        Duration offset10Min = Duration.ofMinutes(10);
        Duration offset1Hour = Duration.ofHours(1);
        String namespace = UUID.randomUUID().toString();

        Gauge gauge1 = encryptionMetricsRegistry.registerNekRotationAgeDelta(namespace, Uuids.startOf(now.minus(offset10Min).toEpochMilli()));
        double measurement1 = gauge1.measure().iterator().next().getValue();

        Gauge gauge2 = encryptionMetricsRegistry.registerNekRotationAgeDelta(namespace, Uuids.startOf(now.minus(offset1Hour).toEpochMilli()));
        double measurement2 = gauge2.measure().iterator().next().getValue();

        Assertions.assertEquals(gauge1, gauge2);
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge1));

        assertThat(measurement1).isCloseTo(
                (double) offset10Min.toMillis(), Offset.offset((double) Duration.ofMinutes(1).toMillis()));

        assertThat(measurement2).isCloseTo(
                (double) offset1Hour.toMillis(), Offset.offset((double) Duration.ofMinutes(1).toMillis()));
    }


    @Test
    void removeNekRotationAgeDelta_onSingleNamespace_removesGauge() {
        String namespace = UUID.randomUUID().toString();
        Gauge gauge = encryptionMetricsRegistry.registerNekRotationAgeDelta(namespace, Uuids.timeBased());
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge));

        encryptionMetricsRegistry.removeNekRotationAgeDelta(namespace);

        Assertions.assertTrue(meterRegistry.getMeters().isEmpty());
        Assertions.assertTrue(encryptionMetricsRegistry.getNamespaceToCreatedAtAndGaugeMap().isEmpty());
    }


    @Test
    void removeNekRotationAgeDelta_onAll_removesAllGauges() {
        String namespace1 = UUID.randomUUID().toString();
        String namespace2 = UUID.randomUUID().toString();
        Gauge gauge1 = encryptionMetricsRegistry.registerNekRotationAgeDelta(namespace1, Uuids.timeBased());
        Gauge gauge2 = encryptionMetricsRegistry.registerNekRotationAgeDelta(namespace2, Uuids.timeBased());
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge1));
        Assertions.assertTrue(meterRegistry.getMeters().contains(gauge2));

        encryptionMetricsRegistry.removeNekRotationAgeDelta();

        Assertions.assertTrue(meterRegistry.getMeters().isEmpty());
        Assertions.assertTrue(encryptionMetricsRegistry.getNamespaceToCreatedAtAndGaugeMap().isEmpty());
    }


    @Test
    void recordGetEncryptionNekV2() {
        encryptionMetricsRegistry.recordGetEncryptionNekV2(UUID.randomUUID().toString(), UUID.randomUUID().toString());
    }

    @Test
    void recordGetDecryptionNekV2() {
        encryptionMetricsRegistry.recordGetDecryptionNekV2(UUID.randomUUID().toString(),
                UUID.randomUUID().toString());
    }


    @Test
    void recordNekRotationAgeWarning() {
        encryptionMetricsRegistry.recordNekRotationAgeWarning(UUID.randomUUID().toString());
    }

    @Test
    void recordNekRotationAgeCritical() {
        encryptionMetricsRegistry.recordNekRotationAgeCritical(UUID.randomUUID().toString());
    }
}
