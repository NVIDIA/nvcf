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
package com.nvidia.ess.encryption.it;

import static com.nvidia.ess.encryption.crypto.CryptoTestUtils.encode;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.atLeastOnce;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.annotation.RunOnlyIfForcedFromCLI;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.ImmutableTableProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.ReencryptionProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.RotationProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.AllowListEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.CompatibleEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.DefaultKeyEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptedPastDurationPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptionKeyPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.RotatedPastDurationPredicate;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationResult;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.KeyMustExistException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import com.nvidia.ess.encryption.exceptions.shaded.BootResponseException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2PartitionRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.testing.TestUtils;
import com.nvidia.ess.encryption.util.EncryptionKeyGenerator;
import java.security.NoSuchAlgorithmException;
import java.text.ParseException;
import java.time.Duration;
import java.time.Instant;
import java.time.temporal.ChronoUnit;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Predicate;
import java.util.stream.Stream;
import org.opentest4j.AssertionFailedError;
import lombok.AllArgsConstructor;
import lombok.Getter;
import lombok.NonNull;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.RandomStringUtils;
import org.apache.commons.lang3.tuple.Pair;
import org.apache.commons.lang3.tuple.Triple;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.AssertionFailureBuilder;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.ArgumentCaptor;
import org.mockito.ArgumentMatcher;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import org.springframework.test.util.ReflectionTestUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.function.Tuple2;
import reactor.util.function.Tuple3;

@Slf4j
@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.rollout.enabled:true",
        "encryption.rollout.useDefaultKey:false",
        "encryption.rollout.useAllowList:false"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
@SuppressWarnings("java:S7467")
class BaseEncryptionKeyServiceIT {

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private EncryptionKeyRotationService encryptionKeyRotationService;

    @Autowired
    private EncryptionKeyReencryptionService encryptionKeyReencryptionService;

    @MockitoSpyBean
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @MockitoSpyBean
    private EncryptionProperties encryptionProperties;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @MockitoSpyBean
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Autowired
    private EncryptionKeyRepository encryptionKeyRepository;

    @Autowired
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @MockitoSpyBean
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    @Autowired
    private EncryptionKeyV2PartitionRepository encryptionKeyV2PartitionRepository;

    @MockitoSpyBean
    private EncryptionKeyCustomRepository encryptionKeyCustomRepository;

    @MockitoSpyBean
    private KeyValidationExecutor keyValidationExecutor;

    private final CryptoProperties cryptoPropertiesStub = new CryptoProperties();

    private final ImmutableTableProperties immutableTablePropertiesStub = new ImmutableTableProperties();

    private RotationProperties rotationPropertiesStub;
    private ReencryptionProperties reencryptionPropertiesStub;

    @BeforeAll
    static void verifyType(@Autowired @Qualifier("encryptionKeyService")
            EncryptionKeyService encryptionKeyService1,
            @Autowired @Qualifier("encryptionKeyRotationService")
                    EncryptionKeyRotationService encryptionKeyRotationService1,
            @Autowired @Qualifier("encryptionKeyReencryptionService")
                    EncryptionKeyReencryptionService encryptionKeyReencryptionService1) {
        assertTrue(encryptionKeyService1 instanceof BaseEncryptionKeyService);
        assertFalse(DefaultKeyEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));
        assertFalse(AllowListEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));
        assertFalse(CompatibleEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));

        assertTrue(encryptionKeyRotationService1 instanceof BaseEncryptionKeyService);
        assertFalse(AllowListEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyRotationService1.getClass()));
        assertFalse(CompatibleEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyRotationService1.getClass()));

        assertTrue(encryptionKeyReencryptionService1 instanceof BaseEncryptionKeyService);
        assertFalse(AllowListEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyReencryptionService1.getClass()));
        assertFalse(CompatibleEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyReencryptionService1.getClass()));
    }

    @BeforeEach
    void setUp() {

        // Mock the `ImmutableTableProperties` configuration inside each test. The stub
        // configuration will be modified inside each test.
        doReturn(immutableTablePropertiesStub).when(encryptionProperties).getImmutableTable();

        cryptoPropertiesStub.setMasterKey(cryptoPropertiesHolder.get().getMasterKey());
        cryptoPropertiesStub.setAllMasterKeys(cryptoPropertiesHolder.get().getAllMasterKeys());
        cryptoPropertiesStub.setEncryptionMetricsRegistry(encryptionMetricsRegistry);
        cryptoPropertiesStub.setEncryptionProperties(encryptionProperties);

        try {
            cryptoPropertiesStub.init();
        } catch (ParseException ex) {
            Assertions.fail(ex);
        }

        // `CryptoProperties` cannot be a spy bean. A stub-instance needs to be set inside
        // `BaseEncryptionKeyService` instead, which can then be manipulated inside each test.
        //
        // Instead of making `EncryptionKeyService` a spy bean, just make the
        // `RefreshScopedBeanHolder<CryptoProperties>` bean a spy bean and mock
        // its `get()` method to return the stub-instance of `CryptoProperties`
        // instead.
        doReturn(cryptoPropertiesStub).when(cryptoPropertiesHolder).get();

        rotationPropertiesStub = deepCopy(encryptionProperties.getRotation());
        doReturn(rotationPropertiesStub)
                .when(encryptionProperties).getRotation();


        reencryptionPropertiesStub = deepCopy(encryptionProperties.getReencryption());
        doReturn(reencryptionPropertiesStub)
                .when(encryptionProperties).getReencryption();
    }

    private RotationProperties deepCopy(RotationProperties original) {
        return RotationProperties.builder()
                        .enabled(original.isEnabled())
                        .scheduled(original.getScheduled().toBuilder().build())
                        .alwaysRotateList(new ArrayList<>(original.getAlwaysRotateList()))
                        .compliancePeriod(original.getCompliancePeriod())
                .build();
    }


    private ReencryptionProperties deepCopy(ReencryptionProperties original) {
        return ReencryptionProperties.builder()
                .enabled(original.isEnabled())
                .scheduled(original.getScheduled().toBuilder().build())
                .allowList(new ArrayList<>(original.getAllowList()))
                .build();
    }

    @AfterEach
    void tearDown() {
        // Delete all data in between test-runs.
        encryptionKeyRepository.deleteAll().block();
        encryptionKeyByTimestampRepository.deleteAll().block();
        encryptionKeyV2Repository.deleteAll().block();
    }

    private Mono<Tuple3<OctetSequenceKey, OctetSequenceKey, EncryptionKeyModel>>
            getKeyCachedTwiceAndUncachedOnce(String namespace, boolean lookInsideNEKv2Table) {

        return encryptionKeyService.getEncryptionKey(namespace)
                .flatMap(newKey -> Mono.zip(Mono.just(newKey),
                        encryptionKeyService.getEncryptionKey(namespace),
                        lookInsideNEKv2Table
                                ? crudEncryptionKeyService.getKeyUncachedV2(namespace,
                                                TestUtils::alwaysTrueErrorReportingPredicate)
                                        .map(v2Model -> v2Model.toEncryptionKeyByKidModel())
                                : crudEncryptionKeyService.getKeyUncached(namespace)));
    }

    private static EncryptionKeyV2Model nekV2ModelFromV1Model(EncryptionKeyModel nekV1Model,
            @NonNull String currentKid, @NonNull String status) {

        return EncryptionKeyV2Model.builder()
                .namespace(nekV1Model.getNamespace())
                .kid(nekV1Model.getKid())
                .encryptedAt(nekV1Model.getEncryptedAt())
                .currentKid(currentKid)
                .createdAt(nekV1Model.getCreatedAt())
                .encryptedKey(nekV1Model.getEncryptedKey())
                .encryptedByKid(nekV1Model.getEncryptedByKid())
                .status(status)
                .build();
    }

    // TODO: Test each test-case below with the following test-scenarios:
    //
    // 1) NEKv2 writes disabled (during NEK creation as part of secret-write), NEKv2 reads enabled (during
    //    secret-decryption as part of secret-read).
    // 2) NEKv2 writes disabled, NEKv2 reads disabled.
    // 3) NEKv2 writes enabled, NEKv2 reads enabled.

    private static Stream<Arguments> keyCrudTestArguments() {
        return Stream.of(
            // <NEKv2 write disabled, NS not in NEKv2 write allowlist, NEKv2 read disabled, NEKv1 fallback-read impossible [no NEKv2 read]>
            // Keys are written to NEKv1 and reads are done only on NEKv1.
            Arguments.of(false, false, false, false),

            // <NEKv2 write disabled, NS not in NEKv2 write allowlist, NEKv2 read enabled, NEKv1 fallback-read enabled>
            // Keys are written to NEKv1 and reads to NEKv2 are enabled and fallback-read of NEKv1 is allowed.
            Arguments.of(false, false, true, true),

            // <NEKv2 write disabled, NS in NEKv2 write allowlist, NEKv2 read enabled, NEKv1 fallback-read enabled>
            // Keys are written to NEKv2 and reads to NEKv2 are enabled and fallback-read of NEKv1 is allowed.
            Arguments.of(false, true, true, true),

            // <NEKv2 write enabled, NS not in NEKv2 write allowlist, NEKv2 read enabled, NEKv1 fallback-read enabled>
            // Keys are written to NEKv2, reads to NEKv2 are enabled and fallback-read of NEKv1 is allowed.
            Arguments.of(true, false, true, true),

            // <NEKv2 write enabled, NS not in NEKv2 write allowlist, NEKv2 read enabled, NEKv1 fallback-read disabled>
            // Keys are written to NEKv2 and read from NEKv2. A fallback read of NEKv1 is not allowed.
            Arguments.of(true, false, true, false)
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @Order(1)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionKey_onNewMasterKeyWithinGracePeriod_shouldEncryptUsingPreviousMek(boolean nekV2WritesEnabled,
            boolean namespaceInNEKv2WriteAllowlist, boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;
        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        OctetSequenceKey currentMek = cryptoPropertiesStub.getActualParsedMasterKey();
        doReturn(Duration.ofDays(10)).when(encryptionProperties).getMekRotationGracePeriod();
        // set different master key, but keep old one in the full list
        var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
        cryptoPropertiesStub.setAllMasterKeys(encode(
                List.of(currentMek.toJSONObject(), newMasterKey.toJSONObject())));
        cryptoPropertiesStub.init();

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        var newAndFetchedKey = getKeyCachedTwiceAndUncachedOnce(namespace, namespaceNEKv2WriteEnabled);

        StepVerifier.create(newAndFetchedKey)
                .assertNext(tuple -> {
                    Assertions.assertEquals(tuple.getT1(), tuple.getT2());
                    Assertions.assertEquals(tuple.getT2().getKeyID(), tuple.getT3().getKid());
                    Assertions.assertEquals(currentMek.getKeyID(), tuple.getT3().getEncryptedByKid());
                })
                .expectComplete()
                .verify();

    }


    @SneakyThrows
    @ParameterizedTest
    @Order(2)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionKey_onOnly1NewMasterKeyWithinGracePeriod_shouldEncryptUsingNewMek(boolean nekV2WritesEnabled,
            boolean namespaceInNEKv2WriteAllowlist, boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        doReturn(Duration.ofDays(10)).when(encryptionProperties).getMekRotationGracePeriod();
        // set different master key, but keep old one in the full list
        var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
        cryptoPropertiesStub.setAllMasterKeys(encode(List.of(newMasterKey.toJSONObject())));
        cryptoPropertiesStub.init();

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        var newAndFetchedKey = getKeyCachedTwiceAndUncachedOnce(namespace, namespaceNEKv2WriteEnabled)
                        // cleanup
                        .flatMap(tuple3 -> Mono.zip(
                                        encryptionKeyV2Repository.delete(
                                                nekV2ModelFromV1Model(tuple3.getT3(), tuple3.getT3().getKid(),
                                                        EncryptionKeyStatus.CREATION_VALIDATED.name())
                                        ).thenReturn(true),
                                        encryptionKeyRepository.delete(tuple3.getT3()).thenReturn(true),
                                        encryptionKeyByTimestampRepository.delete(tuple3.getT3()
                                                .toEncryptionKeyByTimestampModel()).thenReturn(true)
                                )
                                .flatMap(deleteResultTuple -> Mono.just(Pair.of(tuple3, deleteResultTuple)))
                        );

        StepVerifier.create(newAndFetchedKey)
                .assertNext(tuple -> {
                    Assertions.assertEquals(tuple.getLeft().getT1(), tuple.getLeft().getT2());
                    Assertions.assertEquals(tuple.getLeft().getT2().getKeyID(), tuple.getLeft().getT3().getKid());
                    Assertions.assertEquals(newMasterKey.getKeyID(), tuple.getLeft().getT3().getEncryptedByKid());
                })
                .expectComplete()
                .verify();

        verify(crudEncryptionKeyService).addKey(any(EncryptionKeyV2Model.class));
    }

    @SneakyThrows
    @ParameterizedTest
    @Order(3)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionKey_onNewMasterKeyOutsideGracePeriod_shouldEncryptUsingNewMek(boolean nekV2WritesEnabled,
            boolean namespaceInNEKv2WriteAllowlist, boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        OctetSequenceKey currentMek = cryptoPropertiesStub.getActualParsedMasterKey();
        // Negative duration absorbs WSL2 wall-clock backward jumps.
        doReturn(Duration.ofSeconds(-10)).when(encryptionProperties).getMekRotationGracePeriod();
        // set different master key, but keep old one in the full list
        var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
        cryptoPropertiesStub.setAllMasterKeys(encode(
                List.of(currentMek.toJSONObject(), newMasterKey.toJSONObject())));
        cryptoPropertiesStub.init();

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        var newAndFetchedKey = getKeyCachedTwiceAndUncachedOnce(namespace, namespaceNEKv2WriteEnabled)
                        // cleanup
                        .flatMap(tuple3 -> Mono.zip(
                                        encryptionKeyRepository.delete(tuple3.getT3()).thenReturn(true),
                                        encryptionKeyByTimestampRepository.delete(tuple3.getT3()
                                                .toEncryptionKeyByTimestampModel()).thenReturn(true))
                                .map(tuple2 -> tuple3));

        StepVerifier.create(newAndFetchedKey)
                .assertNext(tuple -> {
                    Assertions.assertEquals(tuple.getT1(), tuple.getT2());
                    Assertions.assertEquals(tuple.getT2().getKeyID(), tuple.getT3().getKid());
                    Assertions.assertEquals(newMasterKey.getKeyID(), tuple.getT3().getEncryptedByKid());
                })
                .expectComplete()
                .verify();

        verify(crudEncryptionKeyService).addKey(any(EncryptionKeyV2Model.class));
    }

    @ParameterizedTest
    @Order(4)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionKey_onRepeatedFetchWithDifferentMasterKey_succeeds(boolean nekV2WritesEnabled,
            boolean namespaceInNEKv2WriteAllowlist, boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        Mono<Tuple2<OctetSequenceKey, OctetSequenceKey>> newAndFetchedKey =
                encryptionKeyService.getEncryptionKey(namespace)
                        .doOnNext(newKey -> {
                            // set different master key, but keep old one in the full list
                            var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
                            cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
                            cryptoPropertiesStub.setAllMasterKeys(encode(
                                    List.of(cryptoPropertiesStub.getActualParsedMasterKey().toJSONObject(),
                                            newMasterKey.toJSONObject())));
                            try {
                                cryptoPropertiesStub.init();
                            } catch (ParseException e) {
                                Assertions.fail(e);
                            }
                        }).flatMap(newKey -> Mono.zip(Mono.just(newKey),
                                // to check that decryption key is fetchable
                                encryptionKeyService.getEncryptionKey(namespace)));

        StepVerifier.create(newAndFetchedKey)
                .assertNext(tuple -> Assertions.assertEquals(tuple.getT1(), tuple.getT2()))
                .expectComplete()
                .verify();

        if (namespaceNEKv2WriteEnabled) {
            verify(encryptionKeyCustomRepository).addKeyV2(any(EncryptionKeyV2Model.class));
        } else {
            verify(encryptionKeyCustomRepository).addKey(any(EncryptionKeyV2Model.class));
        }
    }

    @Test
    @Order(5)
    void getEncryptionKey_onFailingKeyValidation_fails() {
        var customError = new BootResponseException(HttpStatus.INTERNAL_SERVER_ERROR, "", BootResponseException.class) {};
        KeyFetchError keyFetchError = new KeyFetchError(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR, customError);
        String namespace = UUID.randomUUID().toString();
        doReturn(KeyValidationResult.failure(keyFetchError)).when(keyValidationExecutor).validate(any(EncryptionKeyV2Model.class));

        Mono<OctetSequenceKey> newKey =
                encryptionKeyService.getEncryptionKey(namespace);

        StepVerifier.create(newKey)
                .expectErrorMatches(customError::equals)
                .verify();
    }

    @ParameterizedTest
    @Order(6)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionAndDecryptionKey_onDifferentAllMasterKeys_throwsMissingMasterKeyException(
            boolean nekV2WritesEnabled, boolean namespaceInNEKv2WriteAllowlist,
            boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        Mono<OctetSequenceKey> decryptFails =
                encryptionKeyService.getEncryptionKey(namespace)
                        .doOnNext(newKey -> {
                            // set different master decryption keys
                            var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
                            cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
                            cryptoPropertiesStub.setAllMasterKeys(encode(
                                    List.of(newMasterKey.toJSONObject())));
                            try {
                                cryptoPropertiesStub.init();
                            } catch (ParseException e) {
                                Assertions.fail(e);
                            }
                        }).flatMap(newKey -> encryptionKeyService.getDecryptionKey(namespace,
                                newKey.getKeyID()));

        StepVerifier.create(decryptFails)
                .expectError(MissingMasterKeyException.class)
                .verify();

        if (namespaceNEKv2WriteEnabled) {
            verify(encryptionKeyCustomRepository).addKeyV2(any(EncryptionKeyV2Model.class));
        } else {
            verify(encryptionKeyCustomRepository).addKey(any(EncryptionKeyV2Model.class));
        }
    }

    private static Stream<Arguments> booleanTuple2s() {
        return Stream.of(false, true).flatMap(
            v1 -> Stream.of(false, true).map(v2 -> Arguments.of(v1, v2))
        );
    }

    @SneakyThrows
    @ParameterizedTest
    @Order(7)
    @MethodSource("booleanTuple2s")
    void getEncryptionKey_currentKidSetInNEKv2_onNoKeyMatchingCurrentKid_throwsKeyMustExistException(
            boolean nekV2WritesEnabled, boolean nekV1FallbackReadEnabled) {

        String namespace = UUID.randomUUID().toString();


        immutableTablePropertiesStub.setNekv2WriteEnabled(true);
        immutableTablePropertiesStub.setNekv2ReadEnabled(true);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(false);

        // Obtain an encryption-key for a namespace (a new encryption-key gets created in NEKv2).
        var createKeyThenChangeCurrentKid = encryptionKeyService.getEncryptionKey(namespace)
                // Now change `current_kid` to the KID of a key that doesn't exist in storage.
                .flatMap(createdKey -> encryptionKeyV2Repository.findFirstByNamespaceAndKid(namespace, createdKey.getKeyID())
                        .flatMap(keyFromStorage -> {
                            try {
                                var arbitraryKid = EncryptionKeyGenerator.generateEncryptionKey().getKeyID();
                                keyFromStorage.setCurrentKid(arbitraryKid);
                                return encryptionKeyV2PartitionRepository.save(keyFromStorage);
                            } catch (NoSuchAlgorithmException | JOSEException _) {
                                return Mono.error(() -> new AssertionFailedError(
                                        "Illegal execution of `EncryptionKeyGenerator` in test."));
                            }
                        })
                );

        StepVerifier.create(createKeyThenChangeCurrentKid)
                .expectNextCount(1)
                .expectComplete()
                .verify();

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(true);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        // Attempt to obtain an encryption-key for the same namespace again. The attempt should fail with
        // a `KeyMustExistException` with no follow-on attempt to create a new encryption-key.
        var attemptToObtainEncryptionKey = encryptionKeyService.getEncryptionKey(namespace)
                .onErrorResume(ex -> encryptionKeyV2Repository.findAllByNamespace(namespace)
                        .count()
                        .doOnNext(c -> Assertions.assertEquals(1, c))
                        .then(encryptionKeyRepository.findAllByNamespace(namespace)
                                    .count()
                                    .doOnNext(c -> Assertions.assertEquals(0, c)))
                        .then(Mono.error(ex))
                );

        StepVerifier.create(attemptToObtainEncryptionKey)
                .expectError(KeyMustExistException.class)
                .verify();

        // An attempt to obtain a decryption-key with a KID that doesn't match any NEK should still
        // raise `MissingKeyException`.
        var arbitraryKid = EncryptionKeyGenerator.generateEncryptionKey().getKeyID();
        var attemptToObtainDecryptionKey = encryptionKeyService.getDecryptionKey(namespace, arbitraryKid);

        StepVerifier.create(attemptToObtainDecryptionKey)
                .expectError(MissingKeyException.class)
                .verify();
    }

    @Test
    @Order(8)
    void getDecryptionKey_onNonExistingKid_throwsMissingKeyException() {

        // TODO: Test with NEKv2 reads disabled as well as enabled.

        String namespace = UUID.randomUUID().toString();
        String kid = RandomStringUtils.secure().nextAlphabetic(10, 15);

        StepVerifier.create(encryptionKeyService.getDecryptionKey(namespace, kid))
                .expectError(MissingKeyException.class)
                .verify();
    }

    @ParameterizedTest
    @Order(9)
    @MethodSource("keyCrudTestArguments")
    void getEncryptionDecryptionKey_succeeds(boolean nekV2WritesEnabled, boolean namespaceInNEKv2WriteAllowlist,
            boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }
        Mono<Tuple2<OctetSequenceKey, OctetSequenceKey>> newAndDecryptionKey =
                encryptionKeyService.getEncryptionKey(namespace)
                        .flatMap(newKey -> Mono.zip(
                                Mono.just(newKey),
                                // to check that decryption key is fetchable
                                encryptionKeyService.getDecryptionKey(namespace,
                                        newKey.getKeyID())));

        StepVerifier.create(newAndDecryptionKey)
                .assertNext(tuple -> Assertions.assertEquals(tuple.getT1(), tuple.getT2()))
                .expectComplete()
                .verify();

        ArgumentCaptor<EncryptionKeyV2Model> captor = ArgumentCaptor.forClass(EncryptionKeyV2Model.class);
        verify(crudEncryptionKeyService).addKey(captor.capture());

        var addedModel = captor.getValue();
        if (namespaceNEKv2WriteEnabled) {
            verify(encryptionKeyCustomRepository).addKeyV2(addedModel);
        } else {
            verify(encryptionKeyCustomRepository).addKey(addedModel);
        }

        // trick around C* storing only millis since Instant generates nanos as well
        addedModel.setEncryptedAt(addedModel.getEncryptedAt().truncatedTo(ChronoUnit.MILLIS));

        if (namespaceNEKv2WriteEnabled) {
            StepVerifier.create(encryptionKeyV2Repository.findFirstByNamespaceAndKid(
                                addedModel.getNamespace(),
                                addedModel.getKid()))
                    .expectNext(addedModel)
                    .verifyComplete();

            // No row in NEKv1 tables.
            StepVerifier.create(encryptionKeyRepository.findByNamespaceAndKid(
                                addedModel.getNamespace(),
                                addedModel.getKid()))
                    .verifyComplete();

            StepVerifier.create(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                    .verifyComplete();
        } else {
            // No row in NEKv2 table.
            StepVerifier.create(encryptionKeyV2Repository.findFirstByNamespaceAndKid(
                                addedModel.getNamespace(),
                                addedModel.getKid()))
                    .verifyComplete();

            StepVerifier.create(encryptionKeyRepository.findByNamespaceAndKid(
                                addedModel.getNamespace(),
                                addedModel.getKid()))
                    .expectNext(addedModel.toEncryptionKeyByKidModel())
                    .verifyComplete();

            StepVerifier.create(encryptionKeyByTimestampRepository.findFirstByNamespaceOrderByCreatedAtDesc(namespace))
                    .expectNext(addedModel.toEncryptionKeyByTimestampModel())
                    .verifyComplete();
        }

        // verify that metadata of encryption key is correct
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, TestUtils::alwaysTrueErrorReportingPredicate))
                .assertNext(model -> Assertions.assertEquals(
                        cryptoPropertiesStub.getActualParsedMasterKey().getKeyID(),
                        model.getEncryptedByKid()))
                .expectComplete()
                .verify();
    }

    @ParameterizedTest
    @Order(10)
    @MethodSource("keyCrudTestArguments")
    void rotateKey_onNonExistingKey_throwsNotFoundException(boolean nekV2WritesEnabled, boolean namespaceInNEKv2WriteAllowlist,
            boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        StepVerifier.create(encryptionKeyRotationService.rotateEncryptionKey(namespace,
                        new RotatedPastDurationPredicate(Duration.ofSeconds(-5))))
                .expectError(MissingKeyException.class)
                .verify();
    }

    @ParameterizedTest
    @Order(11)
    @MethodSource("keyCrudTestArguments")
    void rotateKey_onExistingKeyAndLongPredicate_doesNotExecuteKeyRotation(boolean nekV2WritesEnabled,
            boolean namespaceInNEKv2WriteAllowlist, boolean nekV2ReadsEnabled, boolean nekV1FallbackReadEnabled) {
        boolean namespaceNEKv2WriteEnabled = nekV2WritesEnabled || namespaceInNEKv2WriteAllowlist;

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        String namespace = UUID.randomUUID().toString();
        if (namespaceInNEKv2WriteAllowlist) {
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of(namespace));
        }

        @AllArgsConstructor
        class ResultOfRotationAttempt {
            public final boolean aRotationWasExecuted;
            public final boolean aRotationExecutionWasSuccessful;
            public final OctetSequenceKey keyBeforeRotationAttempt;
            public final OctetSequenceKey keyAfterRotationAttempt;
        }

        var resultOfRotationAttempt = encryptionKeyService.getEncryptionKey(namespace)
                // attempt rotation (expectation: no execution following the attempt).
                .flatMap(keyBeforeRotationAttempt -> encryptionKeyRotationService.rotateEncryptionKey(
                                namespace,
                                new RotatedPastDurationPredicate(Duration.ofDays(10)))
                        .flatMap(bool -> Mono.zip(Mono.just(bool),
                                // to check that key did not change after rotation execution
                                // (i.e. unsuccessful execution).
                                encryptionKeyService.getEncryptionKey(namespace)))
                        .map(tuple -> new ResultOfRotationAttempt(true, tuple.getT1(),
                                            keyBeforeRotationAttempt, tuple.getT2()))
                        .switchIfEmpty(
                                // to check that key did not change. No rotation was executed following this attempt.
                                encryptionKeyService.getEncryptionKey(namespace)
                                        .map(keyAfterRotationAttempt ->
                                                new ResultOfRotationAttempt(false,false,
                                                keyBeforeRotationAttempt, keyAfterRotationAttempt))
                        )
                );

        StepVerifier.create(resultOfRotationAttempt)
                // .assertNext(tuple -> Assertions.assertEquals(tuple.getT1(), tuple.getT2()))
                .assertNext(result -> {
                    Assertions.assertEquals(false, result.aRotationWasExecuted);
                    Assertions.assertEquals(false, result.aRotationExecutionWasSuccessful);
                    Assertions.assertEquals(result.keyBeforeRotationAttempt, result.keyAfterRotationAttempt);
                })
                .expectComplete()
                .verify();

        if (namespaceNEKv2WriteEnabled) {
            verify(encryptionKeyCustomRepository).addKeyV2(any(EncryptionKeyV2Model.class));
        } else {
            verify(encryptionKeyCustomRepository).addKey(any(EncryptionKeyV2Model.class));
        }
    }

    private static Stream<Arguments> nekRotationTableWriteOptions() {

        // Boolean 4-tuple arguments:
        //
        // <
        //     Prev. key in NEKv2: [true|false],
        //     NEKv2 write enabled for key-rotation: [true|false],
        //     NEKv2 read enabled for key-rotation: [true|false],
        //     NEKv1 fallback-read allowed if NEKv2 read enabled: [true|false]
        // >
        //
        return Stream.of(false, true).flatMap(
                prevKeyInNEKv2 -> Stream.of(false, true)
                        // Filter absurd scenario: [prev-key-in-NEKv2 & rotation-writes-to-NEKv1].
                        .filter(nekV2WritesEnabled -> !prevKeyInNEKv2 || nekV2WritesEnabled)
                        .flatMap(nekV2WritesEnabled -> Stream.of(false, true).flatMap(
                                nekV2ReadsEnabled -> Stream.of(false, true)
                                        // Filter absurd scenario: [NEKv2-read-disabled & NEKv1-fallback-read-enabled].
                                        .filter(nekV1FallbackReadEnabled ->
                                                        nekV2ReadsEnabled || !nekV1FallbackReadEnabled)
                                        .map(nekV1FallbackReadEnabled -> Arguments.of(
                                                prevKeyInNEKv2,
                                                nekV2WritesEnabled,
                                                nekV2ReadsEnabled,
                                                nekV1FallbackReadEnabled
                                        ))
                        )
                )
        );
    }

    private static Stream<Arguments> rotateOneKeyWithPredicateTestArgs() {
        return nekRotationTableWriteOptions()
                .flatMap(tableWriteArgs -> Stream.of(
                            // Rotation enabled, Long-duration predicate, namespace in always-rotate list.
                            Triple.of(true, new RotatedPastDurationPredicate(Duration.ofDays(10)), true),
                            // Rotation disabled, Long-duration predicate, namespace in always-rotate list
                            Triple.of(false, new RotatedPastDurationPredicate(Duration.ofDays(10)), true),
                            // Rotation disabled, Zero-duration predicate, namespace in always-rotate list
                            Triple.of(false, new RotatedPastDurationPredicate(Duration.ofSeconds(-5)), true),
                            // Rotation disabled, Zero-duration predicate, namespace not in always-rotate list
                            Triple.of(false, new RotatedPastDurationPredicate(Duration.ofSeconds(-5)), false)
                    )
                        .map(predicateArgs -> Arguments.of(
                                tableWriteArgs.get()[0],
                                tableWriteArgs.get()[1],
                                tableWriteArgs.get()[2],
                                tableWriteArgs.get()[3],
                                predicateArgs.getLeft(),
                                predicateArgs.getMiddle(),
                                predicateArgs.getRight()
                        ))
                );
    }


    private static Stream<Arguments> reencryptOneKeyTestArgs() {
        return nekRotationTableWriteOptions()
                .flatMap(tableWriteArgs -> Stream.of(
                                        // Re-encryption enabled, namespace not in re-encryption allowlist
                                        Pair.of(true, false),
                                        // Re-encryption disabled, namespace in re-encryption allowlist
                                        Pair.of(false, true)
                                )
                                .map(predicateArgs -> Arguments.of(
                                        tableWriteArgs.get()[0],
                                        tableWriteArgs.get()[1],
                                        tableWriteArgs.get()[2],
                                        tableWriteArgs.get()[3],
                                        predicateArgs.getLeft(),
                                        predicateArgs.getRight()
                                ))
                );
    }


    @AllArgsConstructor
    @Getter
    private static class OneKeyRotationResult {
        @NonNull private final OctetSequenceKey originalKey;
        private final EncryptionKeyV2Model rotatedKeyFromStorage;
        private final Optional<Throwable> rotationError; 
    }

    private static class NEKv2RowMatcher implements ArgumentMatcher<EncryptionKeyV2Model> {

        private final Predicate<EncryptionKeyV2Model> matcher;

        public static NEKv2RowMatcher negate(NEKv2RowMatcher matcher) {
            return new NEKv2RowMatcher(matcher.matcher.negate());
        }

        public static NEKv2RowMatcher anyCreatedNEKInNamespace(String namespace) {
            return new NEKv2RowMatcher(
                    model -> EncryptionKeyStatus.CREATION_VALIDATED.name().equals(model.getStatus())
                                    && namespace.equals(model.getNamespace())
            );
        }

        public static NEKv2RowMatcher anyNEKWithKidAndEncryptedAt(String kid, Instant encryptedAt) {
            return new NEKv2RowMatcher(
                    model -> kid.equals(model.getKid()) && encryptedAt.equals(model.getEncryptedAt())
            );
        }

        public static NEKv2RowMatcher anyRotationNEKInNamespace(String namespace) {
            return new NEKv2RowMatcher(
                    model -> EncryptionKeyStatus.PENDING_ROTATION.name().equals(model.getStatus())
                                    && namespace.equals(model.getNamespace())
            );
        }

        private NEKv2RowMatcher(Predicate<EncryptionKeyV2Model> matcher) {
            this.matcher = matcher;
        }

        @Override
        public boolean matches(EncryptionKeyV2Model argument) {
            return matcher.test(argument);
        }
    }

    private static boolean prevNEKIsVisible(boolean prevNEKInNEKv2, boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled) {
        return
                // An NEK existed in NEKv2 and key-rotation is allowed to see it. A rotated key will be inserted
                // into either NEKv2 or NEKv1 depending on whether NEKv2 writes are enabled.
                (prevNEKInNEKv2 && nekV2ReadsEnabled) ||
                // An NEK existed in NEKv1 and key-rotation is allowed to see it either because NEKv2 reads
                // are enabled WITH fallback to NEKv1 enabled as well, OR NEKv2 reads are disabled so
                // rotation simply proceeds to read NEKv1 instead. As above, a rotated key will be inserted into
                // either NEKv2 or NEKv1 depending on whether NEKv2 writes are enabled.
                (!prevNEKInNEKv2 && (!nekV2ReadsEnabled || nekV1FallbackReadEnabled));
    }

    private static boolean rotationAttempted(boolean prevNEKInNEKv2, boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled, boolean rotationEnabled, boolean namespaceInAlwaysRotateList) {
        return (namespaceInAlwaysRotateList || rotationEnabled) && prevNEKIsVisible(prevNEKInNEKv2,
                nekV2ReadsEnabled, nekV1FallbackReadEnabled);
    }


    private static boolean prevNEKIsVisibleToReencryption(boolean prevNEKInNEKv2, boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled) {

        return prevNEKIsVisible(prevNEKInNEKv2, nekV2ReadsEnabled, nekV1FallbackReadEnabled);
    }

    private Mono<OctetSequenceKey> createAndCacheLoadEncryptionKey(boolean placeNEKInNEKv2, String namespace) {
        return Mono.defer(() -> {

            final var nekv2WriteEnabledOld = immutableTablePropertiesStub.isNekv2WriteEnabled();
            final var nekv2ReadEnabledOld = immutableTablePropertiesStub.isNekv2ReadEnabled();
            final var nekV1FallbackReadEnabledOld = immutableTablePropertiesStub.isNekv1FallbackReadEnabled();

            // Enable NEKv2 write depending on whether the key should go into NEKv2 or NEKv1
            immutableTablePropertiesStub.setNekv2WriteEnabled(placeNEKInNEKv2);
            // Enable get-key operations to load the key into cache irrespective of whether it
            // might be in NEKv1 or NEKv2.
            immutableTablePropertiesStub.setNekv2ReadEnabled(true);
            immutableTablePropertiesStub.setNekv1FallbackReadEnabled(true);

            return encryptionKeyService.getEncryptionKey(namespace)
                    // initial creation of the encryption key will not populate the cache.
                    // Need to populate the cache directly to test the scenario.
                    .then(Mono.defer(() -> encryptionKeyService.getEncryptionKey(namespace)))
                    .doOnTerminate(() -> {
                        // Restore table read/write access properties to the original values.
                        immutableTablePropertiesStub.setNekv2WriteEnabled(nekv2WriteEnabledOld);
                        immutableTablePropertiesStub.setNekv2ReadEnabled(nekv2ReadEnabledOld);
                        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabledOld);
                    });
        });
    }

    private static EncryptionKeyV2Model v1ToV2Model(EncryptionKeyModel v1Model) {
        return EncryptionKeyV2Model.builder()
                .namespace(v1Model.getNamespace())
                .kid(v1Model.getKid())
                .createdAt(v1Model.getCreatedAt())
                .encryptedAt(v1Model.getEncryptedAt())
                // NEKs in NEKv1 don't have to undergo promotion.
                .status(EncryptionKeyStatus.VALIDATED.name())
                // NEK from key-rotation in NEKv1 is automatically 'current' if
                // no validated key in NEKv2 exists for the same namespace.
                .currentKid(v1Model.getKid())
                .encryptedByKid(v1Model.getEncryptedByKid())
                .encryptedKey(v1Model.getEncryptedKey())
                .build();
    }

    private Flux<EncryptionKeyV2Model> queryForMatchingNEKs(String namespace, String kid) {

        return encryptionKeyV2Repository.findAllByNamespaceAndKid(namespace, kid)
                .mergeWith(encryptionKeyRepository.findByNamespaceAndKid(namespace, kid)
                                    .map(v1Model -> v1ToV2Model(v1Model)));
    }

    private Mono<EncryptionKeyV2Model> checkForRotatedKeyAndPromoteAndGet(String namespace,
            Set<String> preRotationKids, boolean nekV2WritesEnabled) {
        
        if (nekV2WritesEnabled) {
            // Key-rotation should have written to NEKv2

            return encryptionKeyV2Repository
                    .findAllByNamespace(namespace)
                    .filter(keyInStorage -> EncryptionKeyStatus.PENDING_ROTATION
                                    .name()
                                    .equals(keyInStorage.getStatus()))
                    .next()
                    .switchIfEmpty(Mono.error(() -> {
                        return AssertionFailureBuilder.assertionFailure()
                                .message("No NEK found inserted by " +
                                        "key-rotation in NEKv2 for namespace: " + namespace)
                                .build();
                    }))
                    .flatMap(rotatedKeyInStorage -> {
                        // Then promote the new NEK created by rotation to `VALIDATED`.
                        //
                        // This is necessary in order for get-key operations to pick up
                        // this new NEK.
                        rotatedKeyInStorage.setStatus(EncryptionKeyStatus.VALIDATED.name());
                        rotatedKeyInStorage.setCurrentKid(rotatedKeyInStorage.getKid());
                        return crudEncryptionKeyService.promoteRotationKey(rotatedKeyInStorage)
                                .flatMap(success -> {
                                    if (success) {
                                        return Mono.just(rotatedKeyInStorage);
                                    }
                                    return Mono.error(() ->
                                            new IllegalStateException("Error occurred during promotion"));
                                });
                    });
            }

            // Key-rotation should have written to NEKv1.
            return encryptionKeyRepository.findAllByNamespace(namespace, CassandraPageRequest.first(100))
                    .expand(slice -> {
                        return !slice.hasNext()
                                ? Mono.empty()
                                : encryptionKeyRepository.findAllByNamespace(namespace,
                                        (CassandraPageRequest) slice.getPageable());
                    })
                    .flatMapIterable(slice -> slice.getContent())
                    .filter(v1Model -> !preRotationKids.contains(v1Model.getKid()))
                    .next()
                    .map(v1Model -> v1ToV2Model(v1Model))
                    .switchIfEmpty(Mono.error(() -> {
                        return AssertionFailureBuilder.assertionFailure()
                                .message("No NEK found inserted by " +
                                        "key-rotation in NEKv1 for namespace: " + namespace)
                                .build();
                    }));
    }

    private Mono<OneKeyRotationResult> getOneKeyRotationResult(String namespace, OctetSequenceKey originalKey,
            boolean prevNEKInNEKv2, boolean nekV2WritesEnabled, boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled, boolean rotationEnabled, boolean namespaceInAlwaysRotateList) {

        var rotationShouldOccur = rotationAttempted(prevNEKInNEKv2, nekV2ReadsEnabled,
                nekV1FallbackReadEnabled, rotationEnabled, namespaceInAlwaysRotateList);

        return Mono.defer(() -> rotationShouldOccur
                        // Obtain the NEK created by rotation (and promote it to VALIDATED status
                        // if it was inserted into NEKv2).
                        ? checkForRotatedKeyAndPromoteAndGet(namespace,
                                        Set.of(originalKey.getKeyID()), nekV2WritesEnabled)
                                .map(keyCreatedByRotation ->
                                        new OneKeyRotationResult(originalKey, keyCreatedByRotation,
                                                Optional.empty())
                                )

                        // Rotation was not meant to have occurred, and no error was
                        // encountered during the attempt.
                        : Mono.just(new OneKeyRotationResult(originalKey, null,
                                Optional.empty()))
                );
    }

    private void verifyNEKPersistsAfterRotation(String namespace, boolean prevNEKInNEKv2, boolean nekV2WritesEnabled,
            boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled) {

        if (prevNEKInNEKv2) {
            // NEK existed in NEKv2 table prior to rotation.

            if (nekV2ReadsEnabled) {
                // Rotation is allowed to read NEKv2.

                if (nekV2WritesEnabled) {
                    // Rotation writes to NEKv2.

                    // No writes to NEKv1.
                    verify(encryptionKeyCustomRepository, times(0))
                            .addKey(any(EncryptionKeyV2Model.class));

                    // 2 writes to NEKv2: One for the original NEK and one for the rotated NEK.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKeyV2(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKeyV2(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                } else {
                    // Rotation writes to NEKv1.

                    // One write to NEKv1 by rotation.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKey(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                    // One write to NEKv2 corresponding to the original NEK's insertion.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKeyV2(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));
                }
            } else {
                // Rotation is allowed to read only NEKv1 even though there is no NEK in NEKv1.

                // No writes to NEKv1.
                verify(encryptionKeyCustomRepository, times(0))
                        .addKey(any(EncryptionKeyV2Model.class));

                // Only 1 write to NEKv2 corresponding to the original NEK's insertion (rotation does nothing).
                verify(encryptionKeyCustomRepository, times(1))
                        .addKeyV2(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));
            }

        } else {
            // NEK existed in NEKv1 table prior to rotation.

            if (nekV2ReadsEnabled) {
                // Rotation is allowed to read NEKv2.

                if (nekV1FallbackReadEnabled) {
                    // Rotation is allowed to read NEKv1 as a fallback.

                    if (nekV2WritesEnabled) {
                        // Rotation writes to NEKv2.

                        // 1 write to NEKv1 corresponding to the original NEK's insertion.
                        verify(encryptionKeyCustomRepository, times(1))
                                .addKey(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));

                        // 1 write to NEKv2 by rotation.
                        verify(encryptionKeyCustomRepository, times(1))
                                .addKeyV2(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                    } else {
                        // Rotation writes to NEKv1.

                        // 2 writes to NEKv1: One for the original NEK and one for the rotated NEK.
                        verify(encryptionKeyCustomRepository, times(1))
                                .addKey(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));
                        verify(encryptionKeyCustomRepository, times(1))
                                .addKey(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                        // No writes to NEKv2.
                        verify(encryptionKeyCustomRepository, times(0))
                                .addKeyV2(any(EncryptionKeyV2Model.class));
                    }


                } else {
                    // Rotation is only allowed to read NEKv2 even though there is no NEK in NEKv2.

                    // Only 1 write to NEKv1 corresponding to the original NEK's insertion (rotation does nothing).
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKey(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));

                    // No writes to NEKv2.
                    verify(encryptionKeyCustomRepository, times(0))
                            .addKeyV2(any(EncryptionKeyV2Model.class));

                }

            } else {
                // Rotation is allowed to read only NEKv1

                if (nekV2WritesEnabled) {
                    // Rotation writes to NEKv2.

                    // 1 write to NEKv1 corresponding to the original NEK's insertion.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKey(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));

                    // 1 write to NEKv2 by rotation.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKeyV2(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                } else {
                    // Rotation writes to NEKv1.

                    // 2 writes to NEKv1: One for the original NEK and one for the rotated NEK.
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKey(argThat(NEKv2RowMatcher.anyCreatedNEKInNamespace(namespace)));
                    verify(encryptionKeyCustomRepository, times(1))
                            .addKey(argThat(NEKv2RowMatcher.anyRotationNEKInNamespace(namespace)));

                    // No writes to NEKv2.
                    verify(encryptionKeyCustomRepository, times(0))
                            .addKeyV2(any(EncryptionKeyV2Model.class));
                }
            }
        }
    }

    @ParameterizedTest
    @Order(12)
    @MethodSource("rotateOneKeyWithPredicateTestArgs")
    void rotateOneKey_onAlwaysRotateOrRotationPredicateMatch_ifOriginalKeyIsReadableAndRotationEnabled_thenRotatesKeyAndKeepsAvailableForDecryption(
            boolean prevNEKInNEKv2,
            boolean nekV2WritesEnabled,
            boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled,
            boolean rotationEnabled,
            EncryptionKeyPredicate predicate,
            boolean namespaceInAlwaysRotateList
    ) {
        rotationPropertiesStub.setEnabled(rotationEnabled);
        var rotationShouldOccur = rotationAttempted(prevNEKInNEKv2, nekV2ReadsEnabled,
                nekV1FallbackReadEnabled, rotationEnabled, namespaceInAlwaysRotateList);

        String namespace = UUID.randomUUID().toString();

        if (namespaceInAlwaysRotateList) {
            rotationPropertiesStub.setAlwaysRotateList(List.of(namespace));
        }

        var createdNEK = new AtomicReference<OctetSequenceKey>();

        // First create a NEK for the namespace in the desired table (NEKv1 or NEKv2).
        var createOneKeyMono = createAndCacheLoadEncryptionKey(prevNEKInNEKv2, namespace)
                .doOnNext(createdNEK::set);

        // Attempt rotation on the created NEK.
        var rotateOneKeyMono = createOneKeyMono
                .map(originalKey -> {
                    // Enable NEKv2 write depending on whether rotation should be able to write to NEKv2.
                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    // Enable NEKv2 read & NEKv1 fallback read for rotation depending on whether rotation
                    // should be able to only read NEKv1, or read NEKv2 and then fallback to NEKv1, or
                    // only read NEKv2 without falling back to NEKv1.
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);
                    return originalKey;
                })
                .flatMap(originalKey -> encryptionKeyRotationService.rotateEncryptionKey(namespace, predicate)
                                .flatMap(keyWasRotated -> {
                                    // Check whether key-rotation occurred in accordance with the expectation.
                                    Assertions.assertEquals(rotationShouldOccur, keyWasRotated);
                                    return Mono.<Void>empty();
                                })
                )
                .doOnTerminate(() -> {
                    // Ensure that downstream checks can read from both tables.
                    immutableTablePropertiesStub.setNekv2ReadEnabled(true);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(true);
                })
                // avoid firing twice
                .share();

        Mono<Tuple3<OneKeyRotationResult, OctetSequenceKey, OctetSequenceKey>> oldAndRotatedAndDecryptionKey =
                rotateOneKeyMono.then(Mono.defer(() -> {
                    
                    var originalKey = createdNEK.get();

                    return getOneKeyRotationResult(namespace, originalKey, prevNEKInNEKv2, nekV2WritesEnabled,
                            nekV2ReadsEnabled, nekV1FallbackReadEnabled, rotationEnabled, namespaceInAlwaysRotateList);
                }))
                .onErrorResume(e -> Mono.just(new OneKeyRotationResult(createdNEK.get(),
                        null, Optional.of(e))))
                .flatMap(rotationResult ->  {

                    // Retrieve the original key from storage via the decryption-cache.
                    // Both tables are readable as per the settings above.
                    return encryptionKeyService.getDecryptionKey(namespace,
                            rotationResult.getOriginalKey().getKeyID()
                    )
                    .flatMap(originalKeyFromDecryptionCache -> {
                        // Now retrieve the encryption-key for the namespace via the encryption-cache.
                        // If a key-rotation occurred, this should be the key created by key-rotation.

                        return Mono.zip(
                            Mono.just(rotationResult),
                            encryptionKeyService.getEncryptionKey(namespace),
                            Mono.just(originalKeyFromDecryptionCache)
                        );
                    });
                });

        StepVerifier.create(oldAndRotatedAndDecryptionKey)
                .assertNext(tuple -> {

                    if (!rotationShouldOccur && (rotationEnabled || namespaceInAlwaysRotateList)) {
                        // The zero-duration key-rotation predicate must always pass on
                        // any NEK. The only reason rotation would fail then would be
                        // because of the inability to find an NEK, which should have
                        // caused a NotFoundException.

                        assertTrue(tuple.getT1().getRotationError().isPresent()
                                && tuple.getT1().getRotationError().get().getClass()
                                            .equals(MissingKeyException.class)
                        );
                    }

                    // original encryption key is the same as the encryption key 
                    // in the encryption cache. Loading the rotated key [if any] will
                    // require invalidating the cache.
                    Assertions.assertEquals(tuple.getT1().getOriginalKey(), tuple.getT2());
                    // original encryption key and decryption key
                    Assertions.assertEquals(tuple.getT1().getOriginalKey(), tuple.getT3());
                })
                .expectComplete()
                .verify();

        // clear cache
        crudEncryptionKeyService.clearEncryptionCache();
        crudEncryptionKeyService.clearDecryptionCache();

        // should be updated
        StepVerifier.create(oldAndRotatedAndDecryptionKey)
                .assertNext(tuple -> {
                    if (rotationShouldOccur) {
                        // original encryption key and rotated encryption key (not previously cached)
                        // are not same
                        Assertions.assertNotEquals(tuple.getT1().getOriginalKey(), tuple.getT2());
                    } else {
                        // No rotation occurred, so the same old original encryption key should have
                        // been cached once again.
                        Assertions.assertEquals(tuple.getT1().getOriginalKey(), tuple.getT2());
                    }
                    // original encryption key and decryption key
                    Assertions.assertEquals(tuple.getT1().getOriginalKey(), tuple.getT3());
                })
                .expectComplete()
                .verify();

        if (rotationEnabled || namespaceInAlwaysRotateList) {
            verifyNEKPersistsAfterRotation(namespace, prevNEKInNEKv2, nekV2WritesEnabled, nekV2ReadsEnabled,
                    nekV1FallbackReadEnabled);
        }

    }

    @ParameterizedTest
    @Order(13)
    @MethodSource("nekRotationTableWriteOptions")
    void rotateAllEncryptionKeys_onException_shouldRotateNothing(
            boolean prevNEKInNEKv2,
            boolean nekV2WritesEnabled,
            boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled
    ) {

        // Enable NEKv2 write depending on whether the original key should go into NEKv2 or NEKv1
        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        final var customException = new EncryptionException("custom exception");

        String namespace = UUID.randomUUID().toString();

        // Ensure to create an NEK first.
        var rotateAllNEKs = encryptionKeyService.getEncryptionKey(namespace)
                .then(Mono.defer(() -> {

                    // Now throw exceptions from the encryption-cache's cache-loader
                    // i.e. `getKeyUncached[V2](namespace)` any time it's called by
                    // key-rotation (which calls it while bypassing the cache).

                    doReturn(Mono.error(customException))
                            .when(crudEncryptionKeyService)
                            .getKeyUncached(anyString());
                    doReturn(Mono.error(customException))
                            .when(crudEncryptionKeyService)
                            .getKeyUncachedV2(anyString(), any());
            
                    // Enable NEKv2 write depending on whether rotation should be able to write to NEKv2.
                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    // Enable NEKv2 read & NEKv1 fallback read for rotation depending on whether rotation
                    // should be able to only read NEKv1, or read NEKv2 and then fallback to NEKv1, or
                    // only read NEKv2 without falling back to NEKv1.
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    // `rotateAllEncryptionKeys(...)` should not be interrupted by an error but
                    // no keys should be rotated either (return-value: Mono.just(0)).
                    return encryptionKeyRotationService.rotateAllEncryptionKeys(
                            new RotatedPastDurationPredicate(Duration.ofSeconds(-5))
                    );
                }));

        StepVerifier.create(rotateAllNEKs)
                .expectNext(0)
                .verifyComplete();

        rotationPropertiesStub.setEnabled(true);
        var rotationAttempted =
                rotationAttempted(prevNEKInNEKv2, nekV2ReadsEnabled, nekV1FallbackReadEnabled, true, false);

        // One NEK-insertion corresponding to the original NEK creation. No new NEKs inserted by rotation.
        verify(crudEncryptionKeyService, times(1)).addKey(any(EncryptionKeyV2Model.class));
        // Rotation records errors surfaced by the encryption-key-fetch during a key-rotation attempt on each
        // namespace and moves on to the next namespace each time.
        verify(encryptionMetricsRegistry, rotationAttempted ? atLeastOnce() : times(0))
                .recordNekRotationError(anyString(), eq(customException.getClass().getName()));
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @SneakyThrows
    @ParameterizedTest
    @Order(14)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onDifferentMekAndLongPredicate_shouldReencryptNothing(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                            .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    var newMek = CryptoTestUtils.generateMasterEncryptionKey();
                    var currentMek = cryptoPropertiesStub.getActualParsedMasterKey();

                    cryptoPropertiesStub.setMasterKey(encode(newMek.toJSONObject()));
                    cryptoPropertiesStub.setAllMasterKeys(encode(List.of(newMek.toJSONObject(), currentMek.toJSONObject())));

                    try {
                        cryptoPropertiesStub.init();
                    } catch (ParseException ex) {
                        return Mono.error(ex);
                    }

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(new EncryptedPastDurationPredicate(
                            Duration.ofDays(10), cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
                argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                        createdNEKFromStorage.get().getEncryptedAt())));

        verify(crudEncryptionKeyService, times(0)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @SneakyThrows
    @ParameterizedTest
    @Order(15)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onOlderMek_shouldReencryptNothing(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                            .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    var currentMek = cryptoPropertiesStub.getActualParsedMasterKey();

                    var newMekWithOlderCreateTime = CryptoTestUtils.generateMasterEncryptionKey(
                            Uuids.unixTimestamp(UUID.fromString(currentMek.getKeyID())) -
                            Duration.ofDays(1).toMillis());

                    cryptoPropertiesStub.setMasterKey(encode(newMekWithOlderCreateTime.toJSONObject()));
                    cryptoPropertiesStub.setAllMasterKeys(encode(List.of(newMekWithOlderCreateTime.toJSONObject(), currentMek.toJSONObject())));

                    try {
                        cryptoPropertiesStub.init();
                    } catch (ParseException ex) {
                        return Mono.error(ex);
                    }

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                            new EncryptedPastDurationPredicate(Duration.ofDays(0),
                            cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
            argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                    createdNEKFromStorage.get().getEncryptedAt())));
    
        verify(crudEncryptionKeyService, times(0)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @ParameterizedTest
    @Order(16)
    @MethodSource("reencryptOneKeyTestArgs")
    void reencrypt_onEncryptedWithNonCompatibleMek_ifOriginalNekVisibleToReencryption_thenReencryptsSingleNek(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled,
        boolean reencryptionEnabled,
        boolean namespaceInAllowList
    ) {
        var nonCompatibleMek = cryptoPropertiesStub.getParsedAllMasterKeys().values()
                .stream()
                .filter(octetSequenceKey -> !isUuidV1(octetSequenceKey.getKeyID()))
                .findFirst();
        var currentMek = cryptoPropertiesStub.getActualParsedMasterKey();

        Assertions.assertTrue(nonCompatibleMek.isPresent());

        // normally not possible as current MEK is required to have a UUIDv1 kid, but assuming a backwards compatibility scenario with some NEK encrypted with a non-UUIDv1 MEK
        ReflectionTestUtils.setField(cryptoPropertiesStub, "parsedMasterKey", nonCompatibleMek.get());
        String namespace = UUID.randomUUID().toString();

        var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);
        reencryptionPropertiesStub.setEnabled(reencryptionEnabled);
        if (namespaceInAllowList) {
            reencryptionPropertiesStub.setAllowList(List.of(namespace));
        }

        StepVerifier.create(encryptionKeyService.getEncryptionKey(namespace)
                                    .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                                                            .next()
                                                            .map(nekFromStorage -> {
                                                                createdNEKFromStorage.set(nekFromStorage);
                                                                return nek;
                                                            })))
                .expectNextCount(1)
                .verifyComplete();

        ReflectionTestUtils.setField(cryptoPropertiesStub, "parsedMasterKey", currentMek);
        crudEncryptionKeyService.clearEncryptionCache();

        immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
        immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        // still able to decrypt using MEK
        StepVerifier.create(encryptionKeyReencryptionService.reencryptAllEncryptionKeys(new EncryptedPastDurationPredicate(Duration.ofSeconds(-5), cryptoPropertiesStub.getActualParsedMasterKey().getKeyID())))
                .expectNextCount(1)
                .verifyComplete();

        // create initial NEK + re-encrypt
        verify(crudEncryptionKeyService, times(1)).addKey(
            argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                    createdNEKFromStorage.get().getEncryptedAt())));
    
        var reencryptionOccurred = prevNEKIsVisibleToReencryption(prevNEKInNEKv2, nekV2ReadsEnabled,
                nekV1FallbackReadEnabled);
        
        var writesByReencryption = reencryptionOccurred ? 1 : 0;

        // 1 NEK in either NEKv1 or NEKv2 prior to re-encryption.
        // If the original NEK was in NEKv1 and re-encryption cannot write to NEKv2, then re-encryption
        // will overwrite the original NEK in NEKv1. Otherwise re-encryption will write an additional row.
        var numRowsAfterReencryption = 1L + (reencryptionOccurred && (prevNEKInNEKv2 || nekV2WritesEnabled) ? 1 : 0);

        verify(crudEncryptionKeyService, times(writesByReencryption)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));

        StepVerifier.create(queryForMatchingNEKs(namespace, createdNEKFromStorage.get().getKid())
                                    .count())
                .expectNext(numRowsAfterReencryption)
                .verifyComplete();
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @ParameterizedTest
    @Order(17)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onSameMek_shouldReencryptNothing(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                            .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                            new EncryptedPastDurationPredicate(Duration.ofSeconds(-5),
                            cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
            argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                    createdNEKFromStorage.get().getEncryptedAt())));
    
        verify(crudEncryptionKeyService, times(0)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @SneakyThrows
    @ParameterizedTest
    @Order(18)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onNewerMekAndFailingAddKey_shouldReencryptNothing(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                            .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    var newMek = CryptoTestUtils.generateMasterEncryptionKey();
                    var currentMek = cryptoPropertiesStub.getActualParsedMasterKey();

                    cryptoPropertiesStub.setMasterKey(encode(newMek.toJSONObject()));
                    cryptoPropertiesStub.setAllMasterKeys(encode(List.of(newMek.toJSONObject(), currentMek.toJSONObject())));

                    try {
                        cryptoPropertiesStub.init();
                    } catch (ParseException ex) {
                        return Mono.error(ex);
                    }

                    doReturn(Mono.just(false))
                            .when(crudEncryptionKeyService)
                            .addKey(any(EncryptionKeyV2Model.class));

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                            new EncryptedPastDurationPredicate(Duration.ofSeconds(-5),
                                    cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
            argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                    createdNEKFromStorage.get().getEncryptedAt())));
    
        var writeAttemptsByReencryption = prevNEKIsVisibleToReencryption(prevNEKInNEKv2, nekV2ReadsEnabled,
                        nekV1FallbackReadEnabled)
                ? 1
                : 0;

        verify(crudEncryptionKeyService, times(writeAttemptsByReencryption)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));

        // All write-attempts by re-encryption (if any) failed. Only the row written during NEK creation
        // is present.
        StepVerifier.create(queryForMatchingNEKs(namespace, createdNEKFromStorage.get().getKid())
                                    .count())
                .expectNext(1L)
                .verifyComplete();
    }

    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @SneakyThrows
    @ParameterizedTest
    @Order(19)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onMekMismatch_shouldReencryptNothing(
        boolean prevNEKInNEKv2,
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                            .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    var newMek = CryptoTestUtils.generateMasterEncryptionKey();

                    cryptoPropertiesStub.setMasterKey(encode(newMek.toJSONObject()));
                    cryptoPropertiesStub.setAllMasterKeys(encode(List.of(newMek.toJSONObject())));

                    try {
                        cryptoPropertiesStub.init();
                    } catch (ParseException ex) {
                        return Mono.error(ex);
                    }

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                            new EncryptedPastDurationPredicate(Duration.ofSeconds(-5),
                                    cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
            argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                    createdNEKFromStorage.get().getEncryptedAt())));
    
        verify(crudEncryptionKeyService, times(0)).addKey(
            argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                    createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));
    }


    // @Disabled("Disabled until CancellationException throws fixed in CI pipeline runs")
    @RunOnlyIfForcedFromCLI
    @SneakyThrows
    @ParameterizedTest
    @Order(20)
    @MethodSource("nekRotationTableWriteOptions")
    void reencrypt_onReencryptionDisabled_shouldReencryptNothing(
            boolean prevNEKInNEKv2,
            boolean nekV2WritesEnabled,
            boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled
    ) {
        reencryptionPropertiesStub.setEnabled(false);
        reencryptionPropertiesStub.setAllowList(Collections.emptyList());
        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNEKInNEKv2);

        var namespace = UUID.randomUUID().toString();
        var createNEK = encryptionKeyService.getEncryptionKey(namespace);

        final var createdNEKFromStorage = new AtomicReference<EncryptionKeyV2Model>();

        var runReencryptionAfterCreatingNEK = createNEK
                .flatMap(nek -> queryForMatchingNEKs(namespace, nek.getKeyID())
                        .next())
                .flatMap(nekFromStorage -> {

                    createdNEKFromStorage.set(nekFromStorage);

                    immutableTablePropertiesStub.setNekv2WriteEnabled(nekV2WritesEnabled);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(nekV2ReadsEnabled);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

                    // set different master key, but keep old one in the full list
                    var newMek = CryptoTestUtils.generateMasterEncryptionKey();
                    var currentMasterKey = cryptoPropertiesStub.getValidMek();
                    cryptoPropertiesStub.setMasterKey(encode(newMek.toJSONObject()));
                    cryptoPropertiesStub.setAllMasterKeys(encode(List.of(currentMasterKey.toJSONObject(), newMek.toJSONObject())));

                    try {
                        cryptoPropertiesStub.init();
                    } catch (ParseException ex) {
                        return Mono.error(ex);
                    }

                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(
                            new EncryptedPastDurationPredicate(Duration.ofSeconds(-5),
                                    cryptoPropertiesStub.getActualParsedMasterKey().getKeyID()));
                });

        StepVerifier.create(runReencryptionAfterCreatingNEK)
                .expectNext(0)
                .verifyComplete();

        verify(crudEncryptionKeyService, times(1)).addKey(
                argThat(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(createdNEKFromStorage.get().getKid(),
                        createdNEKFromStorage.get().getEncryptedAt())));

        verify(crudEncryptionKeyService, times(0)).addKey(
                argThat(NEKv2RowMatcher.negate(NEKv2RowMatcher.anyNEKWithKidAndEncryptedAt(
                        createdNEKFromStorage.get().getKid(), createdNEKFromStorage.get().getEncryptedAt()))));
    }


    private static boolean isUuidV1(String kid) {
        try {
            UUID uuid = UUID.fromString(kid);
            return uuid.version() != 1;
        } catch (IllegalArgumentException _) {
            return false;
        }
    }
}
