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
package com.nvidia.ess.encryption.persistence.services;


import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.CacheProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.ImmutableTableProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.RotationProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.exceptions.CurrentKidCheckException;
import com.nvidia.ess.encryption.exceptions.CurrentKidConditionalSetException;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.KeyStatusUpdateException;
import com.nvidia.ess.encryption.exceptions.UnsetCurrentKidException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2PartitionModel;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2PartitionRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.testing.TestUtils;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import java.time.Instant;
import java.util.Random;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.mockito.junit.jupiter.MockitoSettings;
import org.mockito.quality.Strictness;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.Slice;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
@MockitoSettings(strictness = Strictness.STRICT_STUBS)
class CrudEncryptionKeyServiceTests {

    @Mock
    private EncryptionKeyRepository encryptionKeyRepository;

    @Mock
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    @Mock
    private EncryptionKeyV2PartitionRepository encryptionKeyV2PartitionRepository;

    @Mock
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @Mock
    private EncryptionKeyCustomRepository encryptionKeyCustomRepository;

    @Mock
    private EncryptionProperties encryptionProperties;

    @Mock
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    private final MeterRegistry meterRegistry = new SimpleMeterRegistry();

    @InjectMocks
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @BeforeEach
    void init() {
        crudEncryptionKeyService.setMeterRegistry(meterRegistry);
        when(encryptionProperties.getCache())
                .thenReturn(new CacheProperties());

        crudEncryptionKeyService.initCache();
    }


    @Test
    void addKey_onRepositoryException_throwsEncryptionException() {
        var immutableTableProps = new ImmutableTableProperties();
        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(true);

        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        // TODO: test write to NEKv1 as well (i.e. write-feature-flag disabled in ImmutableTableProperties).
        var customException = new RuntimeException("custom exception") {
        };
        var model = mock(EncryptionKeyV2Model.class);
        when(model.logMessageFormatter())
                .thenReturn(s -> s);
        when(encryptionKeyCustomRepository.addKeyV2(model))
                .thenReturn(Mono.error(() -> customException));

        Mono<Boolean> actualMono = crudEncryptionKeyService.addKey(model);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyCustomRepository).addKeyV2(model);
        verifyNoMoreInteractions(encryptionKeyCustomRepository);
        verifyNoInteractions(encryptionKeyV2Repository, encryptionKeyV2PartitionRepository,
                encryptionKeyRepository, encryptionKeyByTimestampRepository);
    }


    @Test
    void getKey_withNamespaceAndOnRepositoryException_throwsEncryptionException() {
        var customException = new RuntimeException("custom exception") {
        };
        String namespace = UUID.randomUUID().toString();

        var immutableTableProps = new ImmutableTableProperties();
        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(true);

        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        // TODO: test-case with NEKv2 read-failure but NEKv1 fallback read success.
        when(encryptionKeyV2PartitionRepository.findFirstByNamespace(namespace))
                .thenReturn(Mono.error(() -> customException));
        when(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                .thenReturn(Mono.error(() -> customException));

        Mono<EncryptionKeyModel> actualMono = crudEncryptionKeyService.getKey(namespace,
                TestUtils::alwaysTrueErrorReportingPredicate);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyV2PartitionRepository).findFirstByNamespace(namespace);
        verify(encryptionKeyByTimestampRepository).findFirstByNamespaceOrderByCreatedAtDesc(
                namespace);
        verify(encryptionMetricsRegistry).recordGetEncryptionNekV2Error(anyString(), anyString());
        verifyNoMoreInteractions(encryptionKeyV2PartitionRepository, encryptionKeyV2Repository, encryptionKeyRepository,
                encryptionKeyCustomRepository);
    }

    @Test
    void getKey_withNamespaceAndKidAndOnRepositoryException_throwsEncryptionException() {
        var immutableTableProps = new ImmutableTableProperties();
        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(true);

        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        var customException = new RuntimeException("custom exception") {
        };
        String namespace = UUID.randomUUID().toString();
        String kid = UUID.randomUUID().toString();
        when(encryptionKeyRepository.findByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.error(() -> customException));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.error(() -> customException));

        Mono<EncryptionKeyModel> actualMono = crudEncryptionKeyService.getKey(namespace, kid,
                TestUtils::alwaysTrueErrorReportingPredicate);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyRepository).findByNamespaceAndKid(namespace, kid);
        verify(encryptionMetricsRegistry).recordGetDecryptionNekV2Error(anyString(), anyString(), anyString());
        verifyNoMoreInteractions(encryptionKeyByTimestampRepository, encryptionKeyCustomRepository);
    }

    public static Stream<Arguments> nekAgeAndExpectedMetricsArgs() {
        // [alertOffset, rotation period, compliance period, isWarningMetricEmitted, isCriticalMetricEmitted]
        return Stream.of(
                // warning alert, no critical due to offset
                Arguments.of(Duration.ZERO, Duration.ZERO, Duration.ofDays(90), true, false),
                // no warning alert due to offset
                Arguments.of(Duration.ofDays(2), Duration.ZERO, Duration.ofDays(90), false, false),
                // no alerts in the middle
                Arguments.of(Duration.ZERO, Duration.ofDays(10), Duration.ofDays(90), false, false),
                // critical alert
                Arguments.of(Duration.ZERO, Duration.ofDays(10), Duration.ZERO, false, true),
                // "overlapping" warning/critical condition alerts, trigger only critical
                Arguments.of(Duration.ZERO, Duration.ZERO, Duration.ZERO, false, true)
        );
    }

    @ParameterizedTest
    @MethodSource("nekAgeAndExpectedMetricsArgs")
    void getKeyWithFailsafe_withCreatedAtPastRotationPlusOffsetDuration_emitsAgeWarningMetric(Duration alertOffset, Duration rotationPeriod, Duration rotationCompliancePeriod, boolean isWarningMetricEmitted, boolean isCriticalMetricEmitted) {
        var immutableTableProps = new ImmutableTableProperties();
        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(true);

        RotationProperties rotationProperties = new RotationProperties();
        rotationProperties.getScheduled().setPeriod(rotationPeriod);
        rotationProperties.setCompliancePeriod(rotationCompliancePeriod);
        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);
        when(encryptionProperties.getRotation()).thenReturn(rotationProperties);
        when(encryptionProperties.getNekAgeAlertingOffset()).thenReturn(alertOffset);

        EncryptionKeyV2Model nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        String namespace = nek.getNamespace();
        String kid = nek.getKid();
        when(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                .thenReturn(Mono.just(nek.toEncryptionKeyByTimestampModel()));
        when(encryptionKeyV2PartitionRepository.findFirstByNamespace(namespace))
                .thenReturn(Mono.just(nek));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.just(nek));

        Mono<EncryptionKeyModel> actualMono = crudEncryptionKeyService.getKeyWithFailsafe(namespace,
                TestUtils::alwaysTrueErrorReportingPredicate);

        StepVerifier.create(actualMono)
                .expectNext(nek.toEncryptionKeyByKidModel())
                .verifyComplete();
        if (isWarningMetricEmitted) {
            verify(encryptionMetricsRegistry).recordNekRotationAgeWarning(namespace);
        } else {
            verify(encryptionMetricsRegistry, times(0)).recordNekRotationAgeWarning(namespace);
        }

        if (isCriticalMetricEmitted) {
            verify(encryptionMetricsRegistry).recordNekRotationAgeCritical(namespace);
        } else {
            verify(encryptionMetricsRegistry, times(0)).recordNekRotationAgeCritical(namespace);
        }
    }

    @Test
    void findNamespaces_onRepositoryException_throwsEncryptionException() {
        var customException = new RuntimeException("custom exception") {
        };
        CassandraPageRequest page = mock(CassandraPageRequest.class);
        when(encryptionKeyCustomRepository.findAllDistinctNamespacesInNEKv1(page))
                .thenReturn(Mono.error(() -> customException));

        Mono<Slice<String>> actualMono = crudEncryptionKeyService.findNamespaces(page);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyCustomRepository).findAllDistinctNamespacesInNEKv1(page);
        verifyNoMoreInteractions(encryptionKeyByTimestampRepository, encryptionKeyRepository);
    }


    @Test
    void findAllKeys_onRepositoryException_throwsEncryptionException() {
        var customException = new RuntimeException("custom exception") {
        };
        String namespace = UUID.randomUUID().toString();
        CassandraPageRequest page = mock(CassandraPageRequest.class);
        when(encryptionKeyRepository.findAllByNamespace(namespace, page))
                .thenReturn(Mono.error(() -> customException));

        Mono<Slice<EncryptionKeyModel>> actualMono = crudEncryptionKeyService.findAllKeys(namespace, page);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyRepository).findAllByNamespace(namespace, page);
        verifyNoMoreInteractions(encryptionKeyByTimestampRepository, encryptionKeyCustomRepository);
    }


    @Test
    void findAllV2Keys_onRepositoryException_throwsEncryptionException() {
        var customException = new RuntimeException("custom exception") {
        };
        when(encryptionKeyCustomRepository.findAllV2Keys(any(CassandraPageRequest.class)))
                .thenReturn(Mono.error(() -> customException));

        Flux<EncryptionKeyV2Model> actualMono = crudEncryptionKeyService.findAllV2Keys(100, 4);

        StepVerifier.create(actualMono)
                .verifyErrorMatches(expectedEx -> expectedEx instanceof EncryptionException
                        && customException.equals(expectedEx.getCause()));
        verify(encryptionKeyCustomRepository).findAllV2Keys(any(CassandraPageRequest.class));
    }

    private static final Random r = new Random();

    private static String randomAlphanumericString(int length) {
        var s = new StringBuilder();
        for (int i = 0; i < length; ++i) {
            var n = r.nextInt(62);
            if (n < 10) {
                s.append('0' + n);
            } else if (n < 36) {
                s.append('A' + (n - 10));
            } else {
                s.append('a' + (n - 36));
            }
        }
        return s.toString();
    }

    private static EncryptionKeyV2Model createNEKInstance(EncryptionKeyStatus status) {

        return EncryptionKeyV2Model.builder()
                .namespace(UUID.randomUUID().toString())
                .kid(UUID.randomUUID().toString())
                .currentKid(UUID.randomUUID().toString()) // Needs to be set due to being @NonNull.
                .createdAt(Uuids.timeBased())
                .encryptedAt(Instant.now())
                .encryptedByKid(UUID.randomUUID().toString())
                .encryptedKey(randomAlphanumericString(128))
                .status(status.name())
                .build();
    }

    @Test
    void promoteRotationKey_statusAndCurrentKidUpdateError_throwsKeyStatusUpdateException() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek.setCurrentKid(nek.getKid());
        var customException = new RuntimeException("custom exception") {};
        doReturn(Mono.error(() -> customException))
                .when(encryptionKeyCustomRepository)
                .updateStatusAndCurrentKid(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(),
                        nek.getStatus(), nek.getCurrentKid());

        StepVerifier.create(crudEncryptionKeyService.promoteRotationKey(nek))
                .expectError(KeyStatusUpdateException.class)
                .verify();
    }

    @Test
    void promoteKey_statusUpdateError_throwsKeyStatusUpdateException() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        var customException = new RuntimeException("custom exception") {};
        doReturn(Mono.error(() -> customException))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(nek))
                .expectError(KeyStatusUpdateException.class)
                .verify();

        verify(encryptionKeyCustomRepository, times(0)).updateCurrentKidIfNotSet(any(), any());
        verifyNoInteractions(encryptionKeyV2PartitionRepository);
    }

    @Test
    void promoteKey_statusUpdateWasNotApplied_currentKidIsNull_noAttemptToConditionallyUpdateCurrentKid_returnsFalse() {
        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());
        // No need to mock `getCurrentKid()` - NULL by default (and Mockito complains if I do anyway).

        doReturn(Mono.just(false))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectNext(false)
                .verifyComplete();

        verify(encryptionKeyCustomRepository, times(0)).updateCurrentKidIfNotSet(any(), any());
        verifyNoInteractions(encryptionKeyV2PartitionRepository);
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidWasSetAlready_success() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(nek))
                .expectNext(true)
                .verifyComplete();

        verify(encryptionKeyCustomRepository, times(0)).updateCurrentKidIfNotSet(any(), any());
        verifyNoInteractions(encryptionKeyV2PartitionRepository);
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidLwtUpdateError_throwsCurrentKidConditionalSetException() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.getCurrentKid()).thenReturn(null);
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());

        var customException = new RuntimeException("custom exception") {};
        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());
        doReturn(Mono.error(() -> customException))
                .when(encryptionKeyCustomRepository)
                .updateCurrentKidIfNotSet(nek.getNamespace(), nek.getKid());
        doReturn(Mono.error(() -> new AssertionError("This should not execute")))
                .when(encryptionKeyV2PartitionRepository)
                .findFirstByNamespace(any());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectError(CurrentKidConditionalSetException.class)
                .verify();
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidLwtUpdateFinished_errorFromCurrentKidNullCheck_throwsCurrentKidCheckException() {
        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.getCurrentKid()).thenReturn(null);
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());

        var customException = new RuntimeException("custom exception") {};
        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());
        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateCurrentKidIfNotSet(nek.getNamespace(), nek.getKid());
        doReturn(Mono.error(() -> customException))
                .when(encryptionKeyV2PartitionRepository)
                .findFirstByNamespace(nek.getNamespace());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectError(CurrentKidCheckException.class)
                .verify();
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidLwtUpdateFinished_emptyOutputFromCurrentKidNullCheck_throwsUnsetCurrentKidException() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.getCurrentKid()).thenReturn(null);
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());

        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());
        doReturn(Mono.just(false))
                .when(encryptionKeyCustomRepository)
                .updateCurrentKidIfNotSet(nek.getNamespace(), nek.getKid());
        doReturn(Mono.empty())
                .when(encryptionKeyV2PartitionRepository)
                .findFirstByNamespace(nek.getNamespace());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectError(UnsetCurrentKidException.class)
                .verify();        
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidLwtUpdateFinished_currentKidStillNull_throwsUnsetCurrentKidException() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.getCurrentKid()).thenReturn(null);
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());

        var partitionModel = mock(EncryptionKeyV2PartitionModel.class);
        doReturn(null).when(partitionModel).getCurrentKid();

        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());
        doReturn(Mono.just(false))
                .when(encryptionKeyCustomRepository)
                .updateCurrentKidIfNotSet(nek.getNamespace(), nek.getKid());
        doReturn(Mono.just(partitionModel))
                .when(encryptionKeyV2PartitionRepository)
                .findFirstByNamespace(nek.getNamespace());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectError(UnsetCurrentKidException.class)
                .verify();
    }

    @Test
    void promoteKey_statusUpdateSuccessful_currentKidLwtUpdateFinished_currentKidNotNull_success() {

        var nek = createNEKInstance(EncryptionKeyStatus.VALIDATED);

        var mockNEK = mock(EncryptionKeyV2Model.class);
        when(mockNEK.getNamespace()).thenReturn(nek.getNamespace());
        when(mockNEK.getKid()).thenReturn(nek.getKid());
        when(mockNEK.getEncryptedAt()).thenReturn(nek.getEncryptedAt());
        when(mockNEK.getStatus()).thenReturn(nek.getStatus());
        when(mockNEK.getCurrentKid()).thenReturn(null);
        when(mockNEK.logMessageFormatter()).thenReturn(nek.logMessageFormatter());

        var partitionModel = mock(EncryptionKeyV2PartitionModel.class);
        doReturn(UUID.randomUUID().toString()).when(partitionModel).getCurrentKid();

        doReturn(Mono.just(true))
                .when(encryptionKeyCustomRepository)
                .updateStatus(nek.getNamespace(), nek.getKid(), nek.getEncryptedAt(), nek.getStatus());
        doReturn(Mono.just(false))
                .when(encryptionKeyCustomRepository)
                .updateCurrentKidIfNotSet(nek.getNamespace(), nek.getKid());
        doReturn(Mono.just(partitionModel))
                .when(encryptionKeyV2PartitionRepository)
                .findFirstByNamespace(nek.getNamespace());

        StepVerifier.create(crudEncryptionKeyService.promoteKey(mockNEK))
                .expectNext(true)
                .verifyComplete();
    }

    @Test
    void getEncryptionKey_onTriggeringRefreshWithinExpiry_shouldRefreshNewValueWithoutBlocking() {
        encryptionProperties.getCache().getEncryption().setTtl(Duration.ofDays(1));
        encryptionProperties.getCache().getEncryption().setRefreshAfterWrite(Duration.ofNanos(1));
        crudEncryptionKeyService.initCache();

        var immutableTableProps = new ImmutableTableProperties();

        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(false);

        RotationProperties rotationProperties = new RotationProperties();
        rotationProperties.setCompliancePeriod(Duration.ZERO);
        when(encryptionProperties.getRotation()).thenReturn(rotationProperties);
        when(encryptionProperties.getNekAgeAlertingOffset()).thenReturn(Duration.ZERO);
        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        EncryptionKeyV2Model nek1 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek1.setCurrentKid(nek1.getKid());
        EncryptionKeyV2Model nek2 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek2.setCurrentKid(nek2.getKid());
        String namespace = nek1.getNamespace();
        nek2.setNamespace(namespace);

        when(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                .thenReturn(Mono.empty());
        when(encryptionKeyV2PartitionRepository.findFirstByNamespace(namespace))
                .thenReturn(Mono.just(nek1))
                // add delay to simulate I/O latency
                .thenReturn(Mono.delay(Duration.ofMillis(100)).thenReturn(nek2));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(nek1.getNamespace(), nek1.getKid()))
                .thenReturn(Mono.just(nek1));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(nek2.getNamespace(), nek2.getKid()))
                .thenReturn(Mono.just(nek2));

        // blocked: fetch nek1 and return nek1
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();


        // not blocked: fetch nek2 and return nek1
        //  wait for the fetch of nek2 to complete
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace,
                        TestUtils::alwaysTrueErrorReportingPredicate)
                        .delayElement(Duration.ofMillis(200)))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();

        // not blocked: fetch nek2 (again) and return nek2
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek2.toEncryptionKeyByKidModel())
                .verifyComplete();
    }

    @Test
    void getEncryptionKey_onTriggeringExpiry_shouldLoadNewValue() {
        encryptionProperties.getCache().getEncryption().setTtl(Duration.ofNanos(1));
        encryptionProperties.getCache().getEncryption().setRefreshAfterWrite(Duration.ofNanos(1));
        crudEncryptionKeyService.initCache();

        var immutableTableProps = new ImmutableTableProperties();

        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(false);

        RotationProperties rotationProperties = new RotationProperties();
        rotationProperties.setCompliancePeriod(Duration.ZERO);
        when(encryptionProperties.getRotation()).thenReturn(rotationProperties);
        when(encryptionProperties.getNekAgeAlertingOffset()).thenReturn(Duration.ZERO);
        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        EncryptionKeyV2Model nek1 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek1.setCurrentKid(nek1.getKid());
        EncryptionKeyV2Model nek2 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek2.setCurrentKid(nek2.getKid());
        String namespace = nek1.getNamespace();
        nek2.setNamespace(namespace);

        when(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                .thenReturn(Mono.empty());
        when(encryptionKeyV2PartitionRepository.findFirstByNamespace(namespace))
                .thenReturn(Mono.just(nek1))
                .thenReturn(Mono.just(nek2));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(nek1.getNamespace(), nek1.getKid()))
                .thenReturn(Mono.just(nek1));
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(nek2.getNamespace(), nek2.getKid()))
                .thenReturn(Mono.just(nek2));

        // blocked: fetch nek1 and return nek1
        //  wait for scheduler to evict cache
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace,
                        TestUtils::alwaysTrueErrorReportingPredicate)
                        .delayElement(Duration.ofMillis(10)))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();

        // evicted already
        // blocked: fetch nek2 and return nek2
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek2.toEncryptionKeyByKidModel())
                .verifyComplete();
    }

    @Test
    void getDecryptionKey_onTriggeringRefreshWithinExpiry_shouldRefreshNewValueWithoutBlocking() {
        encryptionProperties.getCache().getDecryption().setTtl(Duration.ofDays(1));
        encryptionProperties.getCache().getDecryption().setRefreshAfterWrite(Duration.ofNanos(1));
        crudEncryptionKeyService.initCache();

        var immutableTableProps = new ImmutableTableProperties();

        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(false);
        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        EncryptionKeyV2Model nek1 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        String namespace = nek1.getNamespace();
        String kid = nek1.getKid();
        nek1.setCurrentKid(kid);
        EncryptionKeyV2Model nek2 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek2.setKid(kid);
        nek2.setCurrentKid(kid);
        nek2.setNamespace(namespace);

        when(encryptionKeyRepository.findByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.empty());
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.just(nek1))
                // add delay to simulate I/O latency
                .thenReturn(Mono.delay(Duration.ofMillis(100)).thenReturn(nek2));

        // blocked: fetch nek1 and return nek1
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, kid,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();

        // not blocked: fetch nek2 and return nek1
        //  wait for the fetch of nek2 to complete
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, kid,
                                TestUtils::alwaysTrueErrorReportingPredicate)
                        .delayElement(Duration.ofMillis(200)))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();

        // not blocked: fetch nek2 (again) and return nek2
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, kid,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek2.toEncryptionKeyByKidModel())
                .verifyComplete();
    }

    @Test
    void getDecryptionKey_onTriggeringExpiry_shouldLoadNewValue() {
        encryptionProperties.getCache().getDecryption().setTtl(Duration.ofNanos(1));
        encryptionProperties.getCache().getDecryption().setRefreshAfterWrite(Duration.ofNanos(1));
        crudEncryptionKeyService.initCache();

        var immutableTableProps = new ImmutableTableProperties();

        immutableTableProps.setNekv2WriteEnabled(true);
        immutableTableProps.setNekv2ReadEnabled(true);
        immutableTableProps.setNekv1FallbackReadEnabled(false);
        when(encryptionProperties.getImmutableTable()).thenReturn(immutableTableProps);

        EncryptionKeyV2Model nek1 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        String namespace = nek1.getNamespace();
        String kid = nek1.getKid();
        nek1.setCurrentKid(kid);
        EncryptionKeyV2Model nek2 = createNEKInstance(EncryptionKeyStatus.VALIDATED);
        nek2.setKid(kid);
        nek2.setCurrentKid(kid);
        nek2.setNamespace(namespace);

        when(encryptionKeyRepository.findByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.empty());
        when(encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, kid))
                .thenReturn(Mono.just(nek1))
                .thenReturn(Mono.just(nek2));

        // blocked: fetch nek1 and return nek1
        //  wait for scheduler to evict cache
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, kid,
                                TestUtils::alwaysTrueErrorReportingPredicate)
                        .delayElement(Duration.ofMillis(10)))
                .expectNext(nek1.toEncryptionKeyByKidModel())
                .verifyComplete();

        // evicted already
        // blocked: fetch nek2 and return nek2
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, kid,
                        TestUtils::alwaysTrueErrorReportingPredicate))
                .expectNext(nek2.toEncryptionKeyByKidModel())
                .verifyComplete();
    }
}
