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

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.google.gson.JsonParser;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.ImmutableTableProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.PromotionProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyPromotionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptedPastDurationPredicate;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationResult;
import com.nvidia.ess.encryption.crypto.key.validation.OctetSequenceKeyValidator;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.KeyFetchError;
import com.nvidia.ess.encryption.exceptions.shaded.BootResponseException;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository.CurrentKidWriteAction;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import java.text.ParseException;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Stream;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.codec.binary.Base64;
import org.apache.commons.codec.binary.StringUtils;
import org.apache.commons.lang3.tuple.Pair;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@Slf4j
@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.immutableTable.nekv2WriteEnabled:true",
        "encryption.immutableTable.nekv2ReadEnabled:true",
        "encryption.immutableTable.nekv1FallbackReadEnabled:true"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class EncryptionKeyPromotionServiceIT {

    @MockitoSpyBean
    private EncryptionKeyPromotionService encryptionKeyPromotionService;

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private EncryptionKeyReencryptionService encryptionKeyReencryptionService;

    @MockitoSpyBean
    private EncryptionProperties encryptionProperties;

    @MockitoSpyBean
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @MockitoSpyBean
    private KeyValidationExecutor keyValidationExecutor;

    @MockitoSpyBean
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @Autowired
    private EncryptionKeyRepository encryptionKeyRepository;

    @Autowired
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @Autowired
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    @MockitoSpyBean
    private EncryptionKeyCustomRepository encryptionKeyCustomRepository;

    private ImmutableTableProperties immutableTablePropertiesStub;

    private PromotionProperties promotionPropertiesStub;

    private final CryptoProperties cryptoPropertiesStub = new CryptoProperties();

    @BeforeEach
    void setUp() {

        immutableTablePropertiesStub = new ImmutableTableProperties();
        immutableTablePropertiesStub.setNekV2WriteAllowList(Collections.unmodifiableList(
                encryptionProperties.getImmutableTable().getNekV2WriteAllowList()));
        immutableTablePropertiesStub.setNekv2WriteEnabled(
                encryptionProperties.getImmutableTable().isNekv2WriteEnabled());
        immutableTablePropertiesStub.setNekv2ReadEnabled(
                encryptionProperties.getImmutableTable().isNekv2ReadEnabled());
        immutableTablePropertiesStub.setNekv1FallbackReadEnabled(
                encryptionProperties.getImmutableTable().isNekv1FallbackReadEnabled());

        doReturn(immutableTablePropertiesStub).when(encryptionProperties).getImmutableTable();

        promotionPropertiesStub = new PromotionProperties();
        promotionPropertiesStub.setEnabled(encryptionProperties.getPromotion().isEnabled());
        promotionPropertiesStub.setAllowList(Collections.unmodifiableList(
                encryptionProperties.getPromotion().getAllowList()));
        promotionPropertiesStub.setValidationPayloads(Collections.unmodifiableList(
                encryptionProperties.getPromotion().getValidationPayloads()));
        promotionPropertiesStub.setScheduled(encryptionProperties.getPromotion().getScheduled());

        doReturn(promotionPropertiesStub).when(encryptionProperties).getPromotion();

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
    }

    @Test
    @Order(1)
    void promoteEncryptionKey_promotionGloballyDisabled_nsNotInAllowList_statusNotYetPromoted_returnsEmpty() {

        promotionPropertiesStub.setEnabled(false);
        promotionPropertiesStub.setAllowList(List.of("test-ns1"));

        var model = Mockito.mock(EncryptionKeyV2Model.class);
        when(model.getStatus())
                .thenReturn(EncryptionKeyStatus.CREATION_VALIDATED.name());
        when(model.getNamespace()).thenReturn("test-ns2");

        StepVerifier.create(encryptionKeyPromotionService.promoteEncryptionKey(model))
                .verifyComplete();

        verify(model, times(1)).getStatus();
        verify(model, times(1)).getNamespace();
        verifyNoMoreInteractions(model);
    }

    @Test
    @Order(2)
    void promoteEncryptionKey_promotionGloballyEnabled_onValidStatus_returnsEmpty() {

        promotionPropertiesStub.setEnabled(true);

        var model = Mockito.mock(EncryptionKeyV2Model.class);
        when(model.getStatus())
                .thenReturn(EncryptionKeyStatus.VALIDATED.name());

        StepVerifier.create(encryptionKeyPromotionService.promoteEncryptionKey(model))
                .verifyComplete();

        verify(model, times(1)).getStatus();
        verifyNoMoreInteractions(model);
    }

    @Test
    @Order(3)
    void promoteEncryptionKey_promotionGloballyDisabled_nsInAllowList_onValidStatus_returnsEmpty() {

        promotionPropertiesStub.setEnabled(false);
        promotionPropertiesStub.setAllowList(List.of("test-ns1"));

        var model = Mockito.mock(EncryptionKeyV2Model.class);
        when(model.getStatus())
                .thenReturn(EncryptionKeyStatus.VALIDATED.name());

        StepVerifier.create(encryptionKeyPromotionService.promoteEncryptionKey(model))
                .verifyComplete();

        verify(model, times(1)).getStatus();
        verifyNoMoreInteractions(model);
    }

    private static Stream<Arguments> booleanTuple2sAtLeastOneTrue() {
        return Stream.of(false, true)
                .flatMap(v1 ->
                        Stream.of(false, true)
                                .filter(v2 -> v1 || v2)
                                .map(v2 -> Arguments.of(v1, v2))
                );
    }

    @ParameterizedTest
    @Order(4)
    @MethodSource("booleanTuple2sAtLeastOneTrue")
    void promoteEncryptionKey_onAlwaysFailingOctetSequenceKeyValidator_returnsError(boolean promotionGloballyEnabled,
            boolean namespaceInAllowList) {

        String namespace = UUID.randomUUID().toString();

        if (promotionGloballyEnabled) {
            promotionPropertiesStub.setEnabled(true);
        }

        if (namespaceInAllowList) {
            addNamespaceToAllowList(namespace);
        }

        var customException = new BootResponseException(HttpStatus.INTERNAL_SERVER_ERROR, "", BootResponseException.class) {};

        var newValidators = new ArrayList<>(keyValidationExecutor.getOctetJwkValidators());
        var failingValidator = Mockito.mock(OctetSequenceKeyValidator.class);
        when(failingValidator.validate(any(), any()))
                .thenReturn(KeyValidationResult.failure(new KeyFetchError(KeyFetchErrorCode.PROMOTION_VALIDATION_ERROR, customException)));

        newValidators.add(failingValidator);
        when(keyValidationExecutor.getOctetJwkValidators())
            .thenReturn(newValidators);

        StepVerifier.create(generateNEK(namespace, true)
                        .flatMap(model -> encryptionKeyPromotionService.promoteEncryptionKey(model))
                )
                .expectErrorMatches(customException::equals)
                .verify();
    }

    private static Stream<Arguments> statusUpdateResponseAndPromotionError() {
        return Stream.of(
                Arguments.of(Mono.just(false), EncryptionException.class),
                Arguments.of(Mono.error(new RuntimeException("some error"){}), EncryptionException.class),
                Arguments.of(Mono.error(new EncryptionException("some error")), EncryptionException.class)
        )
                .flatMap(args -> booleanTuple2sAtLeastOneTrue().map(
                        tuple2 -> Arguments.of(
                            tuple2.get()[0],
                            tuple2.get()[1],
                            args.get()[0],
                            args.get()[1]
                        )
                ));
    }

    @ParameterizedTest
    @MethodSource("statusUpdateResponseAndPromotionError")
    @Order(5)
    void promoteEncryptionKey_onStatusUpdateResponse_returnsError(boolean promotionGloballyEnabled,
            boolean namespaceInAllowList, Mono<Boolean> statusUpdateResponse, Class<Exception> thrownError) {

        String namespace = UUID.randomUUID().toString();

        if (promotionGloballyEnabled) {
            promotionPropertiesStub.setEnabled(true);
        }

        if (namespaceInAllowList) {
            addNamespaceToAllowList(namespace);
        }

        doReturn(statusUpdateResponse).when(crudEncryptionKeyService)
                .promoteKey(any(EncryptionKeyV2Model.class));

        StepVerifier.create(generateNEK(namespace, true)
                        .flatMap(model -> encryptionKeyPromotionService.promoteEncryptionKey(model))
                )
                .expectError(thrownError)
                .verify();
    }

    @Test
    @Order(6)
    void promoteAllEncryptionKeys_promotionGloballyDisabled_allowListEmpty_skipJob() {

        promotionPropertiesStub.setEnabled(false);
        promotionPropertiesStub.setAllowList(List.of());

        String namespace = UUID.randomUUID().toString();

        StepVerifier.create(generateNEK(namespace, true).then(encryptionKeyPromotionService.promoteAllEncryptionKeys()))
                .assertNext(pair -> {
                    Assertions.assertEquals(Pair.of(0, 0), pair);
                })
                .verifyComplete();

        verify(crudEncryptionKeyService, times(0)).findAllV2Keys(anyInt(), anyInt());
        verify(encryptionKeyPromotionService, times(0)).promoteEncryptionKey(any());
    }

    @Test
    @Order(7)
    void promoteAllEncryptionKeys_promotionGloballyDisabled_allowListHasDifferentNamespace_skipNEK() {

        promotionPropertiesStub.setEnabled(false);
        promotionPropertiesStub.setAllowList(List.of("some-other-namespace"));

        String namespace = UUID.randomUUID().toString();

        AtomicReference<EncryptionKeyV2Model> keyInStorage = new AtomicReference<>();

        StepVerifier.create(generateNEK(namespace, true).flatMap(model -> {
            keyInStorage.set(model);
            return encryptionKeyPromotionService.promoteAllEncryptionKeys(); 
        }))
                .assertNext(pair -> {
                    Assertions.assertEquals(Pair.of(0, 0), pair);
                })
                .verifyComplete();

        verify(encryptionKeyPromotionService, times(1)).promoteEncryptionKey(keyInStorage.get());
    }

    @Test
    @Order(9)
    void oneNEKInNEKv1_reencryptionWritesToNEKv2_getEncryptionKeyReturnsNEKv1KeyBeforePromotion_getEncryptionKeyReturnsNEKv2KeyAfterPromotion() {

        promotionPropertiesStub.setEnabled(true);

        // Clear table contents so that jobs run only on keys created in this test.
        Mono.zip(encryptionKeyV2Repository.deleteAll(), encryptionKeyRepository.deleteAll(),
                encryptionKeyByTimestampRepository.deleteAll()
        )
                .block();

        var namespace = UUID.randomUUID().toString();
        AtomicReference<EncryptionKeyV2Model> keyInNEKv1 = new AtomicReference<>();
        AtomicReference<EncryptionKeyV2Model> keyInNEKv2 = new AtomicReference<>();

        var writeNEKInNEKv1AndReencrypt = generateNEK(namespace, false)
                // Create an NEK in NEKv1.
                .switchIfEmpty(Mono.error(() -> new AssertionError("Invalid execution of NEK creation")))
                .flatMap(keyInStorage -> {
                    // Obtain the NEK persisted in NEKv1.
                    keyInNEKv1.set(keyInStorage);
                    keyInNEKv1.get().setStatus(EncryptionKeyStatus.CREATION_VALIDATED.name());

                    // Rotate the MEK.
                    var newMEK = rotateMEK();

                    // Enable writes to NEKv2, reads from NEKv2 and fallback reads from NEKv1.
                    immutableTablePropertiesStub.setNekv2WriteEnabled(true);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(true);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(true);

                    // Re-encrypt the NEK in NEKv1 (re-encryption should write a row to NEKv2 with
                    // status: PENDING_REENCRYPTION)
                    //
                    // Negative duration absorbs WSL2 wall-clock backward jumps.
                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(new EncryptedPastDurationPredicate(
                                    Duration.ofSeconds(-5), newMEK.getKeyID()))
                            .switchIfEmpty(Mono.error(() ->
                                    new AssertionError("Invalid execution of re-encryption.")))
                            .doOnNext(numReencryptedKeys -> {
                                Assertions.assertEquals(1, numReencryptedKeys);
                            });                            
                })
                // Look for the NEK written by re-encryption (there should be one) in NEKv2.
                .then(encryptionKeyV2Repository.findAllByNamespace(namespace)
                                .elementAt(0))
                .switchIfEmpty(Mono.error(() -> new AssertionError("Re-encryption did not write a key")))
                .doOnNext(reEncryptedKeyInStorage -> {
                    // Obtain the key persisted in NEKv2 by re-encryption.
                    keyInNEKv2.set(reEncryptedKeyInStorage);

                    // The NEK written to NEKv2 by re-encryption should have the same KID.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(),
                            reEncryptedKeyInStorage.getKid());
                    // The NEK written to NEKv2 by re-encryption should have status: PENDING_REENCRYPTION.
                    Assertions.assertEquals(EncryptionKeyStatus.PENDING_REENCRYPTION.name(),
                            reEncryptedKeyInStorage.getStatus());
                    // `current_kid` should be NULL at this point in time.
                    Assertions.assertEquals(null, reEncryptedKeyInStorage.getCurrentKid());
                });

        StepVerifier.create(writeNEKInNEKv1AndReencrypt)
                .expectNextCount(1)
                .verifyComplete();

        // clear cache
        crudEncryptionKeyService.clearEncryptionCache();
        crudEncryptionKeyService.clearDecryptionCache();

        // Attempt to fetch an encryption key for this namespace. This should succeed.
        var getKeyFromNEKv1 = encryptionKeyService.getEncryptionKey(namespace);
        StepVerifier.create(getKeyFromNEKv1)
                .assertNext(fetchedKey -> {
                    Assertions.assertEquals(keyInNEKv1.get().getKid(), fetchedKey.getKeyID());
                })
                .verifyComplete();

        // Verify that no attempt was made to create a new NEK as part of the encryption-key-fetch attempt.

        // One key added to NEKv1 at the beginning:
        verify(encryptionKeyCustomRepository, times(1)).addKey(keyInNEKv1.get());

        // One key added to NEKv2 by re-encryption:

        // EncryptionKeyCustomRepository.addKeyV2() ignores `current_kid` in the passed argument even though
        // the caller ( CrudEncryptionKeyService.addKey() ) sets it to be the same as `kid`.
        keyInNEKv2.get().setCurrentKid(keyInNEKv2.get().getKid());
        verify(encryptionKeyCustomRepository, times(1))
                .addKeyV2(keyInNEKv2.get(), CurrentKidWriteAction.PERSIST_IF_VALIDATED, false);

        // Verify an empty read attempt of NEKv2 without any errors.
        verify(encryptionMetricsRegistry, times(0)).recordGetEncryptionNekV2(eq(namespace),
                anyString());
        verify(encryptionMetricsRegistry, times(0)).recordGetEncryptionNekV2Error(eq(namespace), any());

        // Verify that it is followed by a successful fallback read attempt of NEKv1.
        verify(encryptionMetricsRegistry, times(1)).recordGetEncryptionNekV1(namespace, 
                Optional.of(keyInNEKv1.get().getKid()));
        verify(encryptionMetricsRegistry, times(0)).recordGetEncryptionNekError(eq(namespace), any());

        // Now promote the re-encrypted key in NEKv2.
        var promoteKeyInNEKv2 = encryptionKeyPromotionService.promoteAllEncryptionKeys()
                .switchIfEmpty(Mono.error(() -> new AssertionError("Invalid execution of promotion")))
                .doOnNext(promotedKeyCounts -> {
                    // Promotion should have promoted one key successfully.
                    Assertions.assertEquals(Pair.of(1, 0), promotedKeyCounts);
                })
                .then(encryptionKeyV2Repository.findAllByNamespace(namespace)
                                .elementAt(0))
                .switchIfEmpty(Mono.error(() ->
                        new AssertionError("Invalid execution of promotion: a key was deleted!")))
                .doOnNext(promotedKeyInStorage -> {
                    // The promoted NEK in NEKv2 should have the same KID.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(),
                            promotedKeyInStorage.getKid());
                    // The promoted NEK in NEKv2 should have status: VALIDATED.
                    Assertions.assertEquals(EncryptionKeyStatus.VALIDATED.name(), promotedKeyInStorage.getStatus());
                    // `current_kid` should be the same as `kid` now.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(), promotedKeyInStorage.getCurrentKid());
                });

        StepVerifier.create(promoteKeyInNEKv2)
                .expectNextCount(1)
                .verifyComplete();

        // Clear cache again.
        crudEncryptionKeyService.clearEncryptionCache();
        crudEncryptionKeyService.clearDecryptionCache();

        // Attempt to fetch an encryption key for this namespace. This should succeed.
        var getKeyFromNEKv2 = encryptionKeyService.getEncryptionKey(namespace);
        StepVerifier.create(getKeyFromNEKv2)
                .assertNext(fetchedKey -> {
                    Assertions.assertEquals(keyInNEKv1.get().getKid(), fetchedKey.getKeyID());
                })
                .verifyComplete();

        // Verify a successful read attempt of NEKv2.
        verify(encryptionMetricsRegistry, times(1)).recordGetEncryptionNekV2(namespace,
                keyInNEKv2.get().getKid());
        verify(encryptionMetricsRegistry, times(0)).recordGetEncryptionNekV2Error(eq(namespace), any());

        // Verify no more NEKv1 read attempts (same call-counts as earlier).
        verify(encryptionMetricsRegistry, times(1)).recordGetEncryptionNekV1(namespace, 
                Optional.of(keyInNEKv1.get().getKid()));
        verify(encryptionMetricsRegistry, times(0)).recordGetEncryptionNekError(eq(namespace), any());
    }

    @Test
    @Order(8)
    void promoteAllEncryptionKeys_oneNEKInNEKv1_reencryptionWritesToNEKv2_promoteReencryptedKeyInNEKv2AndSetCurrentKid() {

        promotionPropertiesStub.setEnabled(true);

        // Clear table contents so that jobs run only on keys created in this test.
        Mono.zip(encryptionKeyV2Repository.deleteAll(), encryptionKeyRepository.deleteAll(),
                encryptionKeyByTimestampRepository.deleteAll()
        )
                .block();

        var namespace = UUID.randomUUID().toString();
        AtomicReference<EncryptionKeyModel> keyInNEKv1 = new AtomicReference<>();

        var createReencryptAndPromote = generateNEK(namespace, false)
                // Create an NEK in NEKv1.
                .switchIfEmpty(Mono.error(() -> new AssertionError("Invalid execution of NEK creation")))
                .flatMap(keyInStorage -> {
                    // Obtain the NEK persisted in NEKv1.
                    keyInNEKv1.set(keyInStorage.toEncryptionKeyByKidModel());

                    // Rotate the MEK.
                    var newMEK = rotateMEK();

                    // Enable writes to NEKv2.
                    immutableTablePropertiesStub.setNekv2WriteEnabled(true);
                    immutableTablePropertiesStub.setNekv2ReadEnabled(true);
                    immutableTablePropertiesStub.setNekv1FallbackReadEnabled(true);

                    // Re-encrypt the NEK in NEKv1 (re-encryption should write a row to NEKv2 with
                    // status: PENDING_REENCRYPTION)
                    //
                    // Negative duration absorbs WSL2 wall-clock backward jumps.
                    return encryptionKeyReencryptionService.reencryptAllEncryptionKeys(new EncryptedPastDurationPredicate(
                                    Duration.ofSeconds(-5), newMEK.getKeyID()))
                            .switchIfEmpty(Mono.error(() ->
                                    new AssertionError("Invalid execution of re-encryption.")))
                            .doOnNext(numReencryptedKeys -> {
                                Assertions.assertEquals(1, numReencryptedKeys);
                            });                            
                })
                // Look for the NEK written by re-encryption (there should be one) in NEKv2.
                .then(encryptionKeyV2Repository.findAllByNamespace(namespace)
                                .elementAt(0))
                .switchIfEmpty(Mono.error(() -> new AssertionError("Re-encryption did not write a key")))
                .doOnNext(reEncryptedKeyInStorage -> {
                    // The NEK written to NEKv2 by re-encryption should have the same KID.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(),
                            reEncryptedKeyInStorage.getKid());
                    // The NEK written to NEKv2 by re-encryption should have status: PENDING_REENCRYPTION.
                    Assertions.assertEquals(EncryptionKeyStatus.PENDING_REENCRYPTION.name(),
                            reEncryptedKeyInStorage.getStatus());
                    // `current_kid` should be NULL at this point in time.
                    Assertions.assertEquals(null, reEncryptedKeyInStorage.getCurrentKid());
                })
                // Run NEK promotion.
                .then(encryptionKeyPromotionService.promoteAllEncryptionKeys())
                .switchIfEmpty(Mono.error(() -> new AssertionError("Invalid execution of promotion")))
                .doOnNext(promotedKeyCounts -> {
                    // Promotion should have promoted one key successfully.
                    Assertions.assertEquals(Pair.of(1, 0), promotedKeyCounts);
                })
                .then(encryptionKeyV2Repository.findAllByNamespace(namespace)
                                .elementAt(0))
                .switchIfEmpty(Mono.error(() ->
                        new AssertionError("Invalid execution of promotion: a key was deleted!")))
                .doOnNext(promotedKeyInStorage -> {
                    // The promoted NEK in NEKv2 should have the same KID.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(),
                            promotedKeyInStorage.getKid());
                    // The promoted NEK in NEKv2 should have status: VALIDATED.
                    Assertions.assertEquals(EncryptionKeyStatus.VALIDATED.name(), promotedKeyInStorage.getStatus());
                    // `current_kid` should be the same as `kid` now.
                    Assertions.assertEquals(keyInNEKv1.get().getKid(), promotedKeyInStorage.getCurrentKid());
                });

        StepVerifier.create(createReencryptAndPromote)
                .expectNextCount(1)
                .verifyComplete();
    }

    private void addNamespaceToAllowList(String namespace) {
        var currAllowList = promotionPropertiesStub.getAllowList();
        promotionPropertiesStub.setAllowList(Stream.concat(currAllowList.stream(), Stream.of(namespace)).toList());
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

    private Mono<EncryptionKeyV2Model> generateNEK(String namespace, boolean placeKeyInNekV2) {
        return Mono.defer(() -> {

            var prevNekV2WriteSetting = immutableTablePropertiesStub.isNekv2WriteEnabled();
            var prevNekV2WriteAllowList = immutableTablePropertiesStub.getNekV2WriteAllowList();
            var prevNekV2ReadSetting = immutableTablePropertiesStub.isNekv2ReadEnabled();

            immutableTablePropertiesStub.setNekv2WriteEnabled(placeKeyInNekV2);
            immutableTablePropertiesStub.setNekV2WriteAllowList(List.of());
            immutableTablePropertiesStub.setNekv2ReadEnabled(placeKeyInNekV2);

            return encryptionKeyService.getEncryptionKey(namespace)
                    .then(Mono.defer(() -> {

                        immutableTablePropertiesStub.setNekv2WriteEnabled(prevNekV2WriteSetting);
                        immutableTablePropertiesStub.setNekV2WriteAllowList(prevNekV2WriteAllowList);
                        immutableTablePropertiesStub.setNekv2ReadEnabled(prevNekV2ReadSetting);

                        if (placeKeyInNekV2) {
                            return crudEncryptionKeyService.getKeyUncachedV2(namespace,
                                (nekIgnored, errConsumerIgnored) -> true);
                        }

                        return crudEncryptionKeyService.getKeyUncached(namespace)
                                .map(model -> v1ToV2Model(model));
                    }));
        });
    }

    private OctetSequenceKey rotateMEK() {
        var newMek = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesHolder.get().setMasterKey(CryptoTestUtils.encode(newMek.toJSONObject()));
        var newAllMasterKeys = JsonParser.parseString(StringUtils.newStringUtf8(
                Base64.decodeBase64(cryptoPropertiesHolder.get().getAllMasterKeys()))).getAsJsonArray();
        newAllMasterKeys.add(JsonParser.parseString(newMek.toJSONString()));
        cryptoPropertiesHolder.get().setAllMasterKeys(CryptoTestUtils.encode(newAllMasterKeys.toString()));

        try {
            cryptoPropertiesHolder.get().init();
        } catch (ParseException ex) {
            Assertions.fail(ex);
        }

        return newMek;
    }
}
