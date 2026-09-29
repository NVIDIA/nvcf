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

import com.google.gson.JsonParser;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties.ImmutableTableProperties;
import com.nvidia.ess.encryption.constants.EncryptionKeyStatus;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyReencryptionService;
import com.nvidia.ess.encryption.crypto.key.predicate.EncryptionKeyPredicate;
import com.nvidia.ess.encryption.crypto.key.predicate.RotatedPastDurationPredicate;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyByTimestampRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyCustomRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyRepository;
import com.nvidia.ess.encryption.persistence.repositories.EncryptionKeyV2Repository;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.scheduled.KeyReencryptionScheduledService;
import java.text.ParseException;
import java.time.Duration;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Supplier;
import java.util.stream.Collectors;
import java.util.stream.IntStream;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import org.apache.commons.codec.binary.Base64;
import org.apache.commons.codec.binary.StringUtils;
import org.apache.commons.lang3.tuple.Pair;
import org.junit.jupiter.api.AfterEach;
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
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import org.springframework.util.ReflectionUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.function.Tuple3;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = "spring.profiles.active:it")
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class KeyReencryptionScheduledServiceIT {

    private KeyReencryptionScheduledService keyReencryptionScheduledService;

    @MockitoSpyBean
    private EncryptionKeyReencryptionService encryptionKeyService;

    @MockitoSpyBean
    private EncryptionProperties encryptionProperties;

    @MockitoSpyBean
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @Value("${kv.crypto.allMasterKeys}")
    private String loadedAllMasterKeys;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @MockitoSpyBean
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    @Autowired
    private EncryptionKeyRepository encryptionKeyRepository;

    @Autowired
    private EncryptionKeyByTimestampRepository encryptionKeyByTimestampRepository;

    @Autowired
    private EncryptionKeyV2Repository encryptionKeyV2Repository;

    @Autowired
    private EncryptionKeyCustomRepository encryptionKeyCustomRepository;

    private final CryptoProperties cryptoPropertiesStub = new CryptoProperties();

    private final ImmutableTableProperties immutableTablePropertiesStub = new ImmutableTableProperties();

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
        // `KeyReencryptionScheduledService` instead, which can then be manipulated inside each test.
        //
        // Instead of making `EncryptionKeyService` a spy bean, just make the
        // `RefreshScopedBeanHolder<CryptoProperties>` bean a spy bean and mock
        // its `get()` method to return the stub-instance of `CryptoProperties`
        // instead.
        doReturn(cryptoPropertiesStub).when(cryptoPropertiesHolder).get();

        keyReencryptionScheduledService = new KeyReencryptionScheduledService();
        keyReencryptionScheduledService.setEncryptionKeyReencryptionService(encryptionKeyService);
        keyReencryptionScheduledService.setEncryptionProperties(encryptionProperties);
        keyReencryptionScheduledService.setCryptoPropertiesHolder(cryptoPropertiesHolder);
        keyReencryptionScheduledService.setEncryptionMetricsRegistry(encryptionMetricsRegistry);
    }

    private BaseEncryptionKeyService baseEncryptionKeyService() {
        return (BaseEncryptionKeyService) encryptionKeyService;
    }

    @AfterEach
    void tearDown() {
        // Clean up DB after each test-case run.
        encryptionKeyV2Repository.deleteAll().block();
        encryptionKeyRepository.deleteAll().block();
        encryptionKeyByTimestampRepository.deleteAll().block();
    }

    private Pair<AtomicInteger, Map<String, Set<OctetSequenceKey>>> setupEncryptionKeys() {

        var totalCount = new AtomicInteger();
        Map<String, Set<OctetSequenceKey>> namespaceToOldKeyMap = new ConcurrentHashMap<>();

        AtomicLong createCount = new AtomicLong();
        String namespaceSuffix = UUID.randomUUID().toString();
        Flux<OctetSequenceKey> rotationFlux =
                Flux.fromStream(IntStream.range(0, 100).boxed())
                        .flatMap(i -> {
                            String namespace = "namespace-" + namespaceSuffix + "-" + i;
                            Mono<OctetSequenceKey> mono =
                                    // initial creation of the encryption key will not populate the cache.
                                    // Need to populate the cache directly to test the scenario
                                    baseEncryptionKeyService().getEncryptionKey(namespace)
                                            .then(Mono.defer(() -> baseEncryptionKeyService().getEncryptionKey(namespace)));
                            return Mono.zip(Mono.just(namespace), mono);
                        })
                        .doOnNext(tuple -> {
                            namespaceToOldKeyMap.computeIfAbsent(tuple.getT1(), k -> new HashSet<>()).add(tuple.getT2());
                            totalCount.incrementAndGet();
                            createCount.incrementAndGet();
                        })
                        .flatMap(pair -> { 

                            Supplier<Mono<OctetSequenceKey>> rotatePromoteAndGet = () ->
                                    // Rotate the current key in this namespace.
                                    // Negative duration absorbs WSL2 wall-clock backward jumps.
                                    baseEncryptionKeyService().rotateEncryptionKey(pair.getT1(),
                                            new RotatedPastDurationPredicate(Duration.ofSeconds(-5))
                                    )
                                    .filter(Boolean::booleanValue)
                                    .then(encryptionProperties.getImmutableTable().isNekv2WriteEnabled()
                                            // Promote the rotated key in NEKv2 to VALIDATED.
                                            ? encryptionKeyV2Repository.findAllByNamespace(pair.getT1())
                                                    .filter(model -> EncryptionKeyStatus.PENDING_ROTATION
                                                            .name().equals(model.getStatus()))
                                                    .collectList()
                                                    .flatMap(modelList -> {

                                                        Assertions.assertEquals(1, modelList.size());

                                                        var model = modelList.get(0);
                                                        model.setStatus(EncryptionKeyStatus.VALIDATED.name());
                                                        model.setCurrentKid(model.getKid());

                                                        return crudEncryptionKeyService.promoteRotationKey(model);
                                                    })
                                            // If the rotated key is in NEKv1 instead, do nothing.
                                            : Mono.empty()
                                    )
                                    // Pull the rotated & promoted key from storage (without going
                                    // through the encryption-cache) in order to obtain its KID. This
                                    // fetch will obtain the expected key as long as rotation & promotion
                                    // above was successful.
                                    .then(crudEncryptionKeyService.getKeyWithFailsafe(pair.getT1(),
                                                    (nekIgnored, errConsumerIgnored) -> true))
                                    // Load the rotated & promoted key into the decryption-cache using
                                    // its KID and obtain the resulting `OctetSequenceKey`.
                                    .flatMap(uncached -> baseEncryptionKeyService().getDecryptionKey(pair.getT1(),
                                                                    uncached.getKid())
                                    );

                            return Flux.fromStream(IntStream.range(0, 10).boxed())
                                // to populate map of rotated keys, need to rotate and fetch sequentially
                                .concatMap(counter -> rotatePromoteAndGet.get())
                                .doOnNext(octetSequenceKey -> {
                                    namespaceToOldKeyMap.computeIfAbsent(pair.getT1(), k -> new HashSet<>()).add(octetSequenceKey);
                                    totalCount.incrementAndGet();
                                });
                            }
                        );


        StepVerifier.create(rotationFlux)
                .expectNextCount(1000)
                .expectComplete().verify();

        Assertions.assertEquals(100, createCount.get());
        Assertions.assertEquals(1100, totalCount.get());
        verify(crudEncryptionKeyService, times(1100)).addKey(any(EncryptionKeyV2Model.class));

        for (var allKeysInNamespace : namespaceToOldKeyMap.values()) {
            Assertions.assertEquals(11, allKeysInNamespace.size());
            Assertions.assertEquals(11, allKeysInNamespace.stream()
                    .map(OctetSequenceKey::getKeyID).collect(Collectors.toSet()).size());
        }

        return Pair.of(totalCount, namespaceToOldKeyMap);
    }

    private static Stream<Arguments> keyCrudTestArguments() {
        return Stream.of(
            // <NEKv2 write disabled, NEKv2 read disabled, NEKv1 fallback-read impossible [no NEKv2 read]>
            // Keys are written to NEKv1 and reads are done only on NEKv1.
            Arguments.of(false, false, false),

            // <NEKv2 write disabled, NEKv2 read enabled, NEKv1 fallback-read enabled>
            // Keys are written to NEKv1 and reads to NEKv2 are enabled and fallback-read of NEKv1 is allowed.
            Arguments.of(false, true, true),

            // <NEKv2 write enabled, NEKv2 read enabled, NEKv1 fallback-read enabled>
            // Keys are written to NEKv2, reads to NEKv2 are enabled and fallback-read of NEKv1 is allowed.
            Arguments.of(true, true, true),

            // <NEKv2 write enabled, NEKv2 read enabled, NEKv1 fallback-read disabled>
            // Keys are written to NEKv2 and read from NEKv2. A fallback read of NEKv1 is not allowed.
            Arguments.of(true, true, false)
        );
    }

    @ParameterizedTest
    @Order(1)
    @MethodSource("keyCrudTestArguments")
    void setupEncryptionKeys_runTest(
        boolean nekV2WritesEnabled,
        boolean nekV2ReadsEnabled,
        boolean nekV1FallbackReadEnabled
    ) {

        encryptionProperties.getImmutableTable().setNekv2WriteEnabled(nekV2WritesEnabled);
        encryptionProperties.getImmutableTable().setNekv2ReadEnabled(nekV2ReadsEnabled);
        encryptionProperties.getImmutableTable().setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        setupEncryptionKeys();
    }

    @SneakyThrows
    @ParameterizedTest
    @Order(2)
    @MethodSource("keyCrudTestArguments")
    void reencrypt_onImmediatePredicate_reencryptsKeys(boolean nekV2WritesEnabled, boolean nekV2ReadsEnabled,
            boolean nekV1FallbackReadEnabled) {

        encryptionProperties.getImmutableTable().setNekv2WriteEnabled(nekV2WritesEnabled);
        encryptionProperties.getImmutableTable().setNekv2ReadEnabled(nekV2ReadsEnabled);
        encryptionProperties.getImmutableTable().setNekv1FallbackReadEnabled(nekV1FallbackReadEnabled);

        var totalCountAndPreRotationKeys = setupEncryptionKeys();
        
        var totalCount = totalCountAndPreRotationKeys.getLeft();
        var namespaceToOldKeyMap = totalCountAndPreRotationKeys.getRight();

        // generate a new MEK
        var newMek = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesHolder.get().setMasterKey(encode(newMek.toJSONObject()));

        var newAllMasterKeys = JsonParser.parseString(StringUtils.newStringUtf8(Base64.decodeBase64(this.loadedAllMasterKeys))).getAsJsonArray();
        newAllMasterKeys.add(JsonParser.parseString(newMek.toJSONString()));
        cryptoPropertiesHolder.get().setAllMasterKeys(encode(newAllMasterKeys.toString()));
        cryptoPropertiesHolder.get().init();

        // force re-encryption. Negative period absorbs WSL2 wall-clock backward jumps.
        encryptionProperties.getReencryption().getScheduled().setPeriod(Duration.ofSeconds(-5));


        AtomicReference<Integer> capturedReencryptedCount = new AtomicReference<>();

        StepVerifier.create(keyReencryptionScheduledService.reencrypt())
                .consumeNextWith(capturedReencryptedCount::set)
                .verifyComplete();
        int reencryptedCount = capturedReencryptedCount.get();
        // will reencrypt namespaces from other tests as well, can't rely on exact number of namespaces
        long keysAdded = Mockito.mockingDetails(crudEncryptionKeyService).getInvocations().stream()
                .filter(invocation -> invocation.getMethod().equals(
                        ReflectionUtils.findMethod(CrudEncryptionKeyService.class, "addKey",
                                EncryptionKeyV2Model.class)))
                .count();
        Assertions.assertEquals(reencryptedCount, totalCount.get());
        Assertions.assertEquals(1100 + totalCount.get(), keysAdded);

        Flux<Tuple3<Set<OctetSequenceKey>, OctetSequenceKey, Set<OctetSequenceKey>>>
                encryptionAndDecryptionKeys = Flux.fromIterable(namespaceToOldKeyMap.entrySet())
                .flatMap(pair -> Mono.zip(Mono.just(
                                pair.getValue()),
                        baseEncryptionKeyService().getEncryptionKey(pair.getKey()),
                        Flux.fromIterable(pair.getValue())
                                .flatMap(octetSequenceKey -> baseEncryptionKeyService().getDecryptionKey(
                                        pair.getKey(), octetSequenceKey.getKeyID()))
                                .collect(Collectors.toSet()))
                );


        // clear cache
        crudEncryptionKeyService.clearEncryptionCache();
        crudEncryptionKeyService.clearDecryptionCache();

        StepVerifier.create(encryptionAndDecryptionKeys)
                .thenConsumeWhile(tuple ->
                        tuple.getT1().equals(tuple.getT3())
                                && tuple.getT1().contains(tuple.getT2()))
                .expectComplete()
                .verify();
    }


    @SneakyThrows
    @Test
    @Order(3)
    void reencrypt_onRecentlyRotatedMek_shouldSkipReencryption() {

        encryptionProperties.getImmutableTable().setNekv2WriteEnabled(true);
        encryptionProperties.getImmutableTable().setNekv2ReadEnabled(true);
        encryptionProperties.getImmutableTable().setNekv1FallbackReadEnabled(true);

        setupEncryptionKeys();

        // generate a new MEK
        var newMek = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoPropertiesHolder.get().setMasterKey(encode(newMek.toJSONObject()));

        var newAllMasterKeys = JsonParser.parseString(StringUtils.newStringUtf8(Base64.decodeBase64(this.loadedAllMasterKeys))).getAsJsonArray();
        newAllMasterKeys.add(JsonParser.parseString(newMek.toJSONString()));
        cryptoPropertiesHolder.get().setAllMasterKeys(encode(newAllMasterKeys.toString()));
        cryptoPropertiesHolder.get().init();

        encryptionProperties.setMekRotationGracePeriod(Duration.ofDays(7));

        var startNumEncryptionKeyServiceInvocations = Mockito.mockingDetails(encryptionKeyService)
                .getInvocations()
                .stream()
                .count();

        StepVerifier.create(keyReencryptionScheduledService.reencrypt())
                .expectNext(0)
                .verifyComplete();

        var endNumEncryptionKeyServiceInvocations = Mockito.mockingDetails(encryptionKeyService)
                .getInvocations()
                .stream()
                .count();
        Assertions.assertEquals(startNumEncryptionKeyServiceInvocations,
                endNumEncryptionKeyServiceInvocations);
    }



    @Test
    @Order(4)
    void reencrypt_onFailedReencryption_shouldThrowFromScheduledExecution() {

        encryptionProperties.getImmutableTable().setNekv2WriteEnabled(true);
        encryptionProperties.getImmutableTable().setNekv2ReadEnabled(true);
        encryptionProperties.getImmutableTable().setNekv1FallbackReadEnabled(true);

        setupEncryptionKeys();

        var customException = new RuntimeException("custom exception") {
        };
        when(encryptionKeyService.reencryptAllEncryptionKeys(any(EncryptionKeyPredicate.class)))
                .thenReturn(Mono.error(customException));

        StepVerifier.create(keyReencryptionScheduledService.reencrypt())
                .expectErrorMatches(err -> err.equals(customException))
                .verify();

        verify(encryptionMetricsRegistry).recordNekReencryptionError(anyString());
    }
}
