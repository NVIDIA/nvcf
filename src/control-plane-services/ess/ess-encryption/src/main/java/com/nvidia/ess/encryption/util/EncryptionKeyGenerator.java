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
package com.nvidia.ess.encryption.util;

import com.nimbusds.jose.EncryptionMethod;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.KeyOperation;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nimbusds.jose.util.Base64URL;
import java.security.NoSuchAlgorithmException;
import java.util.Set;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import lombok.experimental.UtilityClass;

/**
 * Encryption key (JWK) generator
 */
@UtilityClass
public class EncryptionKeyGenerator {
    private static final String ENCRYPTION_ALGORITHM = "AES";
    private static final Integer ENCRYPTION_KEY_SIZE = 256;

    /**
     * Generate a random encryption key
     *
     * @return  encryption key
     */
    public OctetSequenceKey generateEncryptionKey()
            throws NoSuchAlgorithmException, JOSEException {
        KeyGenerator keyGen = KeyGenerator.getInstance(ENCRYPTION_ALGORITHM);
        keyGen.init(ENCRYPTION_KEY_SIZE);
        SecretKey secretKey = keyGen.generateKey();

        return new OctetSequenceKey.Builder(secretKey)
                .keyIDFromThumbprint()
                .algorithm(EncryptionMethod.A256GCM)
                .keyOperations(Set.of(KeyOperation.ENCRYPT, KeyOperation.DECRYPT))
                .keyUse(KeyUse.ENCRYPTION)
                .build();
    }

    /**
     * Generate encryption key from value
     *
     * @param value  Encryption key value
     * @return       Encryption key
     */
    public OctetSequenceKey generateEncryptionKey(String value) throws JOSEException {
        Base64URL base64Value = Base64URL.from(value);
        byte[] keyBytes = base64Value.decode();

        if (keyBytes.length * 8 < ENCRYPTION_KEY_SIZE ) {
            throw new JOSEException(String.format(
                "Key size must be at least %d bits, got %d bits", ENCRYPTION_KEY_SIZE, keyBytes.length * 8));
        }

        return new OctetSequenceKey.Builder(base64Value)
                .keyIDFromThumbprint()
                .algorithm(EncryptionMethod.A256GCM)
                .keyOperations(Set.of(KeyOperation.ENCRYPT, KeyOperation.DECRYPT))
                .keyUse(KeyUse.ENCRYPTION)
                .build();
    }
}
