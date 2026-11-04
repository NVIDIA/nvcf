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
import static org.mockito.Mockito.verify;

import com.nvidia.ess.encryption.config.properties.DefaultKeyProperties;
import com.nvidia.ess.encryption.crypto.key.AllowListEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.CompatibleEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.DefaultKeyEncryptionKeyService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyService;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyV2Model;
import com.nvidia.ess.encryption.persistence.services.CrudEncryptionKeyService;
import java.util.UUID;
import org.apache.commons.lang3.RandomStringUtils;
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
import reactor.test.StepVerifier;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.rollout.enabled:true",
        "encryption.rollout.useDefaultKey:true",
        "encryption.rollout.useAllowList:false"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class CompatibleEncryptionKeyServiceIT {

    @Autowired
    private EncryptionKeyService encryptionKeyService;

    @Autowired
    private DefaultKeyProperties defaultKeyProperties;

    @MockitoSpyBean
    private CrudEncryptionKeyService crudEncryptionKeyService;

    @BeforeAll
    static void verifyType(@Autowired @Qualifier("encryptionKeyService")
            EncryptionKeyService encryptionKeyService1,
            @Autowired @Qualifier("encryptionKeyRotationService")
                    EncryptionKeyRotationService encryptionKeyRotationService1) {
        assertTrue(encryptionKeyService1 instanceof CompatibleEncryptionKeyService);
        assertFalse(DefaultKeyEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));
        assertFalse(AllowListEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyService1.getClass()));

        assertTrue(encryptionKeyRotationService1 instanceof CompatibleEncryptionKeyService);
        assertFalse(AllowListEncryptionKeyService.class.isAssignableFrom(
                encryptionKeyRotationService1.getClass()));
    }

    @Test
    @Order(1)
    void getDecryptionKey_onNonExistingKid_throwsMissingKeyException() {
        String namespace = UUID.randomUUID().toString();
        String kid = RandomStringUtils.secure().nextAlphabetic(10, 15);

        StepVerifier.create(encryptionKeyService.getDecryptionKey(namespace, kid))
                .expectError(MissingKeyException.class)
                .verify();
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
        verify(crudEncryptionKeyService).addKey(any(EncryptionKeyV2Model.class));
    }

    @Test
    @Order(3)
    void getDecryptionKey_onKidEqualToDefaultKey_succeeds() {
        String namespace = UUID.randomUUID().toString();
        StepVerifier.create(encryptionKeyService.getDecryptionKey(namespace, defaultKeyProperties.getParsedDefaultKey().getKeyID()))
                .expectNextMatches(decryptionKey -> defaultKeyProperties.getParsedAllDefaultKeys().containsValue(decryptionKey))
                .expectComplete()
                .verify();
    }
}
