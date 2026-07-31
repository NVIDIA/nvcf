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
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.config.RefreshScopedBeanHolder;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.CryptoPropertiesHolder;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.scheduled.ReactiveKeyRotatorScheduledService;
import java.time.Duration;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.IntStream;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import org.springframework.util.ReflectionUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.function.Tuple2;
import reactor.util.function.Tuple3;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = "spring.profiles.active:it")
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class KeyRotatorScheduledServiceIT {

    private ReactiveKeyRotatorScheduledService keyRotatorScheduledService;

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private EncryptionKeyRotationService encryptionKeyRotationService;

    @Autowired
    private EncryptionProperties encryptionProperties;

    @Autowired
    @Qualifier(CryptoPropertiesHolder.BEAN_NAME)
    private RefreshScopedBeanHolder<CryptoProperties> cryptoPropertiesHolder;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @MockitoSpyBean
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    private static final Map<String, OctetSequenceKey> namespaceToOldKeyMap =
            new ConcurrentHashMap<>();

    @BeforeEach
    void setup() {
        keyRotatorScheduledService = new ReactiveKeyRotatorScheduledService();
        keyRotatorScheduledService.setEncryptionKeyRotationService(encryptionKeyRotationService);
        keyRotatorScheduledService.setEncryptionProperties(encryptionProperties);
        keyRotatorScheduledService.setCryptoPropertiesHolder(cryptoPropertiesHolder);
    }


    @Test
    @Order(1)
    void setupEncryptionKeys() {
        String namespaceSuffix = UUID.randomUUID().toString();
        Flux<Tuple2<String, OctetSequenceKey>> creationFlux =
                Flux.fromStream(IntStream.range(0, 100).boxed())
                        .flatMap(i -> {
                            String namespace = "namespace-" + namespaceSuffix + "-" + i;
                            Mono<OctetSequenceKey> mono =
                                    // initial creation of the encryption key will not populate the cache.
                                    // Need to populate the cache directly to test the scenario
                                    encryptionKeyService.getEncryptionKey(namespace)
                                            .then(Mono.defer(() -> encryptionKeyService.getEncryptionKey(namespace)));
                            return Mono.zip(Mono.just(namespace), mono);
                        })
                        .doOnNext(tuple -> {
                            namespaceToOldKeyMap.put(tuple.getT1(), tuple.getT2());
                        });

        StepVerifier.create(creationFlux)
                .expectNextCount(100)
                .expectComplete()
                .verify();

        verify(crudEncryptionKeyService, times(100)).addKey(any(EncryptionKeyV2Model.class));
    }

    @Test
    @Order(2)
    void rotate_onImmediatePredicate_rotatesKeysAndKeepsAvailableForDecryption() {
        // force rotation on all
        // Negative period absorbs WSL2 wall-clock backward jumps.
        encryptionProperties.getRotation().getScheduled().setPeriod(Duration.ofSeconds(-5));

        AtomicReference<Integer> capturedRotatedCount = new AtomicReference<>();

        StepVerifier.create(keyRotatorScheduledService.rotate())
                .consumeNextWith(capturedRotatedCount::set)
                .verifyComplete();
        int rotatedCount = capturedRotatedCount.get();
        // will rotate namespaces from other tests as well, can't rely on exact number of namespaces
        long keysAdded = Mockito.mockingDetails(crudEncryptionKeyService).getInvocations().stream()
                .filter(invocation -> invocation.getMethod().equals(
                        ReflectionUtils.findMethod(CrudEncryptionKeyService.class, "addKey",
                                EncryptionKeyModel.class)))
                .count();
        Assertions.assertTrue(rotatedCount >= namespaceToOldKeyMap.size());
        verify(encryptionMetricsRegistry, times(100)).registerNekRotationAgeDelta(any(), any());

        Flux<Tuple3<OctetSequenceKey, OctetSequenceKey, OctetSequenceKey>>
                encryptionAndDecryptionKeys = Flux.fromIterable(namespaceToOldKeyMap.entrySet())
                .flatMap(pair -> Mono.zip(Mono.just(pair.getValue()),
                        encryptionKeyService.getEncryptionKey(pair.getKey()),
                        encryptionKeyService.getDecryptionKey(
                                pair.getKey(), pair.getValue().getKeyID())));

        StepVerifier.create(encryptionAndDecryptionKeys)
                .thenConsumeWhile(tuple ->
                        // verify initial key and "rotated" are the same due to caching
                        tuple.getT1().equals(tuple.getT2())
                                // verify rotated key still accessible
                                && tuple.getT1().equals(tuple.getT3()))
                .expectComplete()
                .verify();


        // clear cache
        crudEncryptionKeyService.clearEncryptionCache();
        crudEncryptionKeyService.clearDecryptionCache();

        // should be refreshed
        StepVerifier.create(encryptionAndDecryptionKeys)
                .thenConsumeWhile(tuple ->
                        // verify initial key and rotated are not the same
                        !tuple.getT1().equals(tuple.getT2())
                                // verify rotated key still accessible
                                && tuple.getT1().equals(tuple.getT3()))
                .expectComplete()
                .verify();

        long keysAddedAfterCacheInvalidation = Mockito.mockingDetails(crudEncryptionKeyService).getInvocations().stream()
                .filter(invocation -> invocation.getMethod().equals(
                        ReflectionUtils.findMethod(CrudEncryptionKeyService.class, "addKey",
                                EncryptionKeyModel.class)))
                .count();
        Assertions.assertEquals(keysAdded, keysAddedAfterCacheInvalidation);

    }
}
