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

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;

import com.nvidia.ess.encryption.config.properties.DefaultKeyProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.key.AllowListEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.DefaultKeyEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import com.nvidia.ess.encryption.testing.TestUtils;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeAll;
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
import org.apache.commons.lang3.RandomUtils;
import reactor.test.StepVerifier;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.rollout.enabled:true",
        "encryption.rollout.useDefaultKey:true",
        "encryption.rollout.useAllowList:true"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class AllowListEncryptionKeyServiceIT {

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private DefaultKeyProperties defaultKeyProperties;

    @Autowired
    private EncryptionProperties encryptionProperties;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @BeforeAll
    static void verifyType(@Autowired @Qualifier("encryptionKeyService")
            EncryptionKeyService encryptionKeyService1,
            @Autowired @Qualifier("encryptionKeyRotationService")
                    EncryptionKeyRotationService encryptionKeyRotationService1) {
        assertTrue(encryptionKeyService1 instanceof AllowListEncryptionKeyService);
        assertFalse(DefaultKeyEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));

        assertTrue(encryptionKeyRotationService1 instanceof AllowListEncryptionKeyService);
    }

    @Test
    @Order(2)
    void getEncryptionDecryptionKey_succeeds() {
        String namespace = UUID.randomUUID().toString();
        StepVerifier.create(encryptionKeyService.getEncryptionKey(namespace)
                        .flatMap(newKey -> encryptionKeyService.getDecryptionKey(namespace,
                                        newKey.getKeyID())
                                .map(newKey::equals)))
                .expectNext(true)
                .expectComplete()
                .verify();

        verify(crudEncryptionKeyService, times(0)).addKey(any(EncryptionKeyV2Model.class));
    }

    @Test
    @Order(3)
    void getEncryptionKey_onNamespaceInAllowList_succeeds() {
        List<String> allowList = encryptionProperties.getRollout().getAllowList();
        int randomIndex = RandomUtils.insecure().randomInt(0, allowList.size());
        String namespace = allowList.get(randomIndex);

        StepVerifier.create(encryptionKeyService.getEncryptionKey(namespace)
                        .filter(newKey -> !defaultKeyProperties.getParsedDefaultKey().equals(newKey))
                        .flatMap(newKey -> encryptionKeyService.getDecryptionKey(namespace,
                                        newKey.getKeyID())
                                .map(newKey::equals)))
                .expectNext(true)
                .expectComplete()
                .verify();

        verify(crudEncryptionKeyService).addKey(any(EncryptionKeyV2Model.class));
    }

    @Test
    @Order(4)
    void getEncryptionKey_onNamespaceNotInAllowList_succeeds() {
        String namespace = UUID.randomUUID().toString();
        Assertions.assertFalse(
                encryptionProperties.getRollout().getAllowList().contains(namespace));

        StepVerifier.create(encryptionKeyService.getEncryptionKey(namespace))
                .expectNext(defaultKeyProperties.getParsedDefaultKey())
                .expectComplete()
                .verify();

        // verify against decryption key too
        StepVerifier.create(encryptionKeyService.getDecryptionKey(namespace,
                        defaultKeyProperties.getParsedDefaultKey().getKeyID()))
                .expectNextMatches(decryptionKey -> defaultKeyProperties.getParsedAllDefaultKeys()
                        .containsValue(decryptionKey))
                .expectComplete()
                .verify();

        // verify key for this namespace does not exist in C*
        StepVerifier.create(crudEncryptionKeyService.getKey(namespace, TestUtils::alwaysTrueErrorReportingPredicate))
                        .expectError(MissingKeyException.class)
                        .verify();

        verify(crudEncryptionKeyService, times(0)).addKey(any(EncryptionKeyV2Model.class));
    }
}
