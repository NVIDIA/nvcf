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
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.google.gson.JsonParser;
import com.nvidia.ess.encryption.config.properties.CryptoProperties;
import com.nvidia.ess.encryption.config.properties.EncryptionProperties;
import com.nvidia.ess.encryption.crypto.CryptoTestUtils;
import com.nvidia.ess.encryption.crypto.key.EncryptionKeyRotationService;
import com.nvidia.ess.encryption.crypto.key.predicate.RotatedPastDurationPredicate;
import com.nvidia.ess.encryption.scheduled.KeyRotatorScheduledService;
import com.nvidia.ess.encryption.scheduled.ServletKeyRotatorScheduledService;
import java.time.Duration;
import lombok.SneakyThrows;
import org.apache.commons.codec.binary.Base64;
import org.apache.commons.codec.binary.StringUtils;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.annotation.DirtiesContext;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.context.bean.override.mockito.MockitoSpyBean;
import org.springframework.test.context.junit.jupiter.SpringExtension;
import org.springframework.test.context.web.WebAppConfiguration;
import reactor.core.publisher.Mono;

@ExtendWith(SpringExtension.class)
@ExtendWith(MockitoExtension.class)
@SpringBootTest(classes = TestApplication.class, properties = {
        "spring.profiles.active:it",
        "encryption.rotation.scheduled.enabled=true",
        "encryption.rotation.scheduled.cron=-",
        "spring.main.web-application-type=servlet"
})
@WebAppConfiguration
@ContextConfiguration
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class ServletKeyRotatorScheduledServiceIT {

    @Autowired
    private KeyRotatorScheduledService keyRotatorScheduledService;

    @MockitoSpyBean
    private EncryptionKeyRotationService encryptionKeyRotationService;

    @Autowired
    private CryptoProperties cryptoProperties;

    @Autowired
    private EncryptionProperties encryptionProperties;

    @Value("${kv.crypto.allMasterKeys}")
    private String loadedAllMasterKeys;

    @Test
    @Order(1)
    void configure_isServletKeyRotatorScheduledService() {
        Assertions.assertTrue(
                keyRotatorScheduledService.getClass().isAssignableFrom(
                        ServletKeyRotatorScheduledService.class));
    }

    @Test
    @Order(2)
    void rotate_onRotationFailure_fails() {
        when(encryptionKeyRotationService.rotateAllEncryptionKeys(any(RotatedPastDurationPredicate.class)))
                .thenReturn(Mono.error(new RuntimeException("some error")));

        Assertions.assertThrows(RuntimeException.class, () -> ((ServletKeyRotatorScheduledService) keyRotatorScheduledService).rotate());
    }

    @Test
    @Order(3)
    void rotate_onEmptyMono_returns0() {
        when(encryptionKeyRotationService.rotateAllEncryptionKeys(any(RotatedPastDurationPredicate.class)))
                .thenReturn(Mono.empty());

        Assertions.assertEquals(0, ((ServletKeyRotatorScheduledService) keyRotatorScheduledService).rotate());
    }

    @SneakyThrows
    @Test
    @Order(4)
    @DirtiesContext
    void reencrypt_onRecentlyRotatedMek_shouldSkipReencryption() {
        // generate a new MEK
        var newMek = CryptoTestUtils.generateMasterEncryptionKey();
        cryptoProperties.setMasterKey(encode(newMek.toJSONObject()));

        var newAllMasterKeys = JsonParser.parseString(StringUtils.newStringUtf8(Base64.decodeBase64(this.loadedAllMasterKeys))).getAsJsonArray();
        newAllMasterKeys.add(JsonParser.parseString(newMek.toJSONString()));
        cryptoProperties.setAllMasterKeys(encode(newAllMasterKeys.toString()));
        cryptoProperties.init();

        encryptionProperties.setMekRotationGracePeriod(Duration.ofDays(7));

        Assertions.assertEquals(0, ((ServletKeyRotatorScheduledService) keyRotatorScheduledService).rotate());


        verifyNoInteractions(encryptionKeyRotationService);
    }

}
