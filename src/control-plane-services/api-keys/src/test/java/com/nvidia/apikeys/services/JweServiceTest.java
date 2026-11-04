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

package com.nvidia.apikeys.services;


import static org.junit.jupiter.api.Assertions.assertNotNull;

import com.nimbusds.jose.EncryptionMethod;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import java.security.NoSuchAlgorithmException;
import java.time.Instant;
import java.time.LocalDateTime;
import java.time.ZoneId;
import java.time.format.DateTimeFormatter;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;

@Slf4j
class JweServiceTest {

    @Test
    void aesKeygen()
            throws NoSuchAlgorithmException {
        // Generate a secret AES key with 256 bits
        KeyGenerator gen = KeyGenerator.getInstance("AES");
        gen.init(256);
        SecretKey aesKey = gen.generateKey();

        DateTimeFormatter dtfDateTime = DateTimeFormatter.ofPattern("uuuuMMddHHmmss");
        LocalDateTime localDate = LocalDateTime.ofInstant(Instant.now(), ZoneId.systemDefault());

        // Convert to JWK format
        JWK jwk = new OctetSequenceKey.Builder(aesKey)
                .keyID("kid-" + dtfDateTime.format(localDate))
                .algorithm(EncryptionMethod.A256GCM)
                .keyUse(KeyUse.ENCRYPTION)
                .build();

        assertNotNull(jwk);
        log.info(jwk.toJSONString());
    }

}
