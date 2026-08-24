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
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyPromotionService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptedPastDurationPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.RotatedPastDurationPredicate;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationExecutor;
import com.nvidia.ess.encryption.crypto.key.validation.KeyValidationResultWithKey;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.scheduled.KeyPromotionScheduledService;
import java.text.ParseException;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.stream.IntStream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.tuple.Pair;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.function.Tuple2;

@Slf4j
@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.promotion.scheduled.enabled=true",
        "encryption.promotion.scheduled.cron=-",
        "spring.main.web-application-type=reactive",
        "encryption.immutableTable.nekv2WriteEnabled:true",
        "encryption.immutableTable.nekv2ReadEnabled:true",
        "encryption.immutableTable.nekv1FallbackReadEnabled:true",
        "encryption.promotion.enabled:true"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class KeyPromotionScheduledServiceIT {

    @Autowired
    private KeyPromotionScheduledService keyPromotionScheduledService;

    @MockitoSpyBean
    private EncryptionKeyPromotionService encryptionKeyPromotionService;

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private EncryptionKeyRotationService encryptionKeyRotationService;

    @Autowired
    private EncryptionKeyReencryptionService encryptionKeyReencryptionService;

    @MockitoSpyBean
    private EncryptionProperties encryptionProperties;

    @Autowired
    private EncryptionKeyV2Repository encryptionKeyV2Repository;


    @MockitoSpyBean
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @Autowired
    private KeyValidationExecutor keyValidationExecutor;

    @MockitoSpyBean
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;


    private final CryptoProperties cryptoPropertiesStub = new CryptoProperties();

    @BeforeEach
    void setUp() {

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


    private Pair<Integer, Map<String, Pair<OctetSequenceKey, OctetSequenceKey>>> createAndRotateEncryptionKeys(int nekCount) {

        var totalCount = new AtomicInteger();
        Map<String, Pair<OctetSequenceKey, OctetSequenceKey>> namespaceToCreatedAndRotatedKeyMap = new ConcurrentHashMap<>();

        AtomicLong createCount = new AtomicLong();
        AtomicLong rotateCount = new AtomicLong();
        String namespaceSuffix = UUID.randomUUID().toString();
        Flux<OctetSequenceKey> rotationFlux =
                Flux.fromStream(IntStream.range(0, nekCount).boxed())
                        .flatMap(i -> {
                            String namespace = "namespace-" + namespaceSuffix + "-" + i;
                            Mono<OctetSequenceKey> mono =
                                    encryptionKeyService.getEncryptionKey(namespace);
                            return Mono.zip(Mono.just(namespace), mono);
                        })
                        .doOnNext(tuple -> {
                            namespaceToCreatedAndRotatedKeyMap.put(tuple.getT1(), Pair.of(tuple.getT2(), null));
                            totalCount.incrementAndGet();
                            createCount.incrementAndGet();
                        })
                        .flatMap(pair ->
                                // Negative duration absorbs WSL2 wall-clock backward jumps.
                                encryptionKeyRotationService.rotateEncryptionKey(pair.getT1(),
                                                new RotatedPastDurationPredicate(Duration.ofSeconds(-5))
                                        )
                                        .filter(Boolean::booleanValue)
                                        .then(encryptionKeyV2Repository.findAllByNamespace(pair.getT1())
                                                .filter(model -> EncryptionKeyStatus.PENDING_ROTATION
                                                        .name().equals(model.getStatus()))
                                                .next()
                                                .switchIfEmpty(Mono.error(new AssertionError("should have a pending_rotation NEK in namespace: " + pair.getT1())))
                                        )
                                        .flatMap(model -> Mono.justOrEmpty(keyValidationExecutor.extractKeyAndValidateHeaders(model)))
                                        .map(KeyValidationResultWithKey::getOctetSequenceKey)
                                        .doOnNext(octetSequenceKey -> {
                                            if (!namespaceToCreatedAndRotatedKeyMap.containsKey(pair.getT1())) {
                                                Assertions.fail("should be populated in namespace: " + pair.getT1());
                                            }
                                            namespaceToCreatedAndRotatedKeyMap.put(pair.getT1(),
                                                    Pair.of(namespaceToCreatedAndRotatedKeyMap.get(pair.getT1()).getLeft(), octetSequenceKey));

                                            totalCount.incrementAndGet();
                                            rotateCount.incrementAndGet();
                                        })

                        );


        StepVerifier.create(rotationFlux)
                .expectNextCount(nekCount)
                .expectComplete().verify();

        Assertions.assertEquals(nekCount, createCount.get());
        Assertions.assertEquals(nekCount, rotateCount.get());
        Assertions.assertEquals(nekCount * 2, totalCount.get());
        verify(crudEncryptionKeyService, times(nekCount * 2)).addKey(any(EncryptionKeyV2Model.class));

        for (var createdAndRotatedPair : namespaceToCreatedAndRotatedKeyMap.values()) {
            Assertions.assertNotNull(createdAndRotatedPair.getLeft());
            Assertions.assertNotNull(createdAndRotatedPair.getRight());
        }

        return Pair.of(totalCount.get(), namespaceToCreatedAndRotatedKeyMap);
    }

    @SneakyThrows
    private OctetSequenceKey insertNewMek() {
        doReturn(Duration.ofSeconds(-10)).when(encryptionProperties).getMekRotationGracePeriod();
        // set different master key, but keep old one in the full list
        var newMasterKey = CryptoTestUtils.generateMasterEncryptionKey();
        var currentMasterKey = cryptoPropertiesStub.getValidMek();
        cryptoPropertiesStub.setMasterKey(encode(newMasterKey.toJSONObject()));
        cryptoPropertiesStub.setAllMasterKeys(encode(List.of(currentMasterKey.toJSONObject(), newMasterKey.toJSONObject())));
        cryptoPropertiesStub.init();
        return newMasterKey;
    }

    private void verifyCurrentKid(Map<String, Pair<OctetSequenceKey, OctetSequenceKey>> namespaceToCreatedAndRotatedKeyMap, boolean isRotationPromoted) {
        // clear encryption cache
        crudEncryptionKeyService.clearEncryptionCache();
        Flux<Tuple2<OctetSequenceKey, OctetSequenceKey>> currentAndExpectedKeys = Flux.fromIterable(namespaceToCreatedAndRotatedKeyMap.entrySet())
                .flatMap(entry -> encryptionKeyService.getEncryptionKey(entry.getKey())
                        .zipWith(Mono.just(isRotationPromoted ? entry.getValue().getRight()
                                : entry.getValue().getLeft())));

        StepVerifier.create(currentAndExpectedKeys)
                .thenConsumeWhile(currentAndExpectedKey -> {
                    Assertions.assertEquals(currentAndExpectedKey.getT2(), currentAndExpectedKey.getT1());
                    return true;
                })
                .verifyComplete();
    }

    private void verifyValidatedStatus(Map<String, Pair<OctetSequenceKey, OctetSequenceKey>> namespaceToCreatedAndRotatedKeyMap) {
        Flux<EncryptionKeyV2Model> fetchAllVersionsOfCreatedAndRotatedKeys = Flux.fromIterable(namespaceToCreatedAndRotatedKeyMap.entrySet())
                .flatMap(entry -> {
                    String namespace = entry.getKey();
                    OctetSequenceKey createdKey = entry.getValue().getLeft();
                    OctetSequenceKey rotatedKey = entry.getValue().getRight();
                    return Flux.merge(
                            encryptionKeyV2Repository.findAllByNamespaceAndKid(namespace, createdKey.getKeyID())
                                .switchIfEmpty(Mono.error(new AssertionError("no NEKs in namespace: " + namespace))),
                            encryptionKeyV2Repository.findAllByNamespaceAndKid(namespace, rotatedKey.getKeyID())
                                .switchIfEmpty(Mono.error(new AssertionError("no NEKs in namespace: " + namespace)))
                    );
                });

        StepVerifier.create(fetchAllVersionsOfCreatedAndRotatedKeys)
                .thenConsumeWhile(
                        model -> {
                            Assertions.assertEquals(EncryptionKeyStatus.VALIDATED.name(), model.getStatus(),
                                    String.format("NEK (namespace: %s, kid: %s, encrypted_at: %s) has status %s instead of %s",
                                            model.getNamespace(), model.getKid(), model.getEncryptedAt(), model.getStatus(), EncryptionKeyStatus.VALIDATED.name()));
                            return true;
                        }
                )
                .verifyComplete();

    }

    @Test
    @Order(1)
    void promote_onCreatedAndRotatedAndReEncryptedNEKs_promotesAllNEKs() {
        Pair<Integer, Map<String, Pair<OctetSequenceKey, OctetSequenceKey>>> addCountAndCreateAndRotateEncryptionKeys = createAndRotateEncryptionKeys(100);
        verifyCurrentKid(addCountAndCreateAndRotateEncryptionKeys.getRight(), false);

        insertNewMek();

        // only CREATION_VALIDATED and VALIDATED NEKs will be re-encrypted
        StepVerifier.create(encryptionKeyReencryptionService.reencryptAllEncryptionKeys(new EncryptedPastDurationPredicate(Duration.ofSeconds(-5),
                        cryptoPropertiesStub.getActualParsedMasterKey().getKeyID())))
                        .expectNext(100)
                        .verifyComplete();

        // CREATION_VALIDATED, PENDING_ROTATION and PENDING_REENCRYPTION get promoted
        StepVerifier.create(keyPromotionScheduledService.promote())
                .expectNext(Pair.of(300, 0))
                .verifyComplete();

        verifyValidatedStatus(addCountAndCreateAndRotateEncryptionKeys.getRight());
        verifyCurrentKid(addCountAndCreateAndRotateEncryptionKeys.getRight(), true);
    }

    @Test
    @Order(2)
    void promote_onUnexpectedFullError_fails() {
        var customException = new RuntimeException("some error") {};
        when(encryptionKeyPromotionService.promoteAllEncryptionKeys())
                .thenReturn(Mono.error(customException));
        StepVerifier.create(keyPromotionScheduledService.promote())
                .expectError(customException.getClass())
                .verify();

        verify(encryptionMetricsRegistry).recordNekPromotionError(anyString());
    }

    @Test
    @Order(3)
    void promote_onIndividualPromotionFailure_promotes0NEKs() {
        var customException = new RuntimeException("some error") {};
        when(encryptionKeyPromotionService.promoteEncryptionKey(any(EncryptionKeyV2Model.class)))
                .thenReturn(Mono.error(customException));
        StepVerifier.create(keyPromotionScheduledService.promote())
                .expectNext(Pair.of(0, 300))
                .verifyComplete();
    }

    @Test
    @Order(4)
    void promote_onPromotionReturningFalse_promotes0NEKs() {
        when(encryptionKeyPromotionService.promoteEncryptionKey(any(EncryptionKeyV2Model.class)))
                .thenReturn(Mono.just(false));
        StepVerifier.create(keyPromotionScheduledService.promote())
                .expectNext(Pair.of(0, 300))
                .verifyComplete();
    }
}
