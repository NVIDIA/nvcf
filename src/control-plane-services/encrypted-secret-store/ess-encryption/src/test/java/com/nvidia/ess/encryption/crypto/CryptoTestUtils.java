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
package com.nvidia.ess.encryption.crypto;

import com.datastax.oss.driver.api.core.uuid.Uuids;
import tools.jackson.databind.json.JsonMapper;
import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.util.EncryptionKeyGenerator;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import lombok.SneakyThrows;

public class CryptoTestUtils {

    private static final JsonMapper objectMapper = JsonMapper.builder().build();

    @SneakyThrows
    public static String encode(String text) {
        return encode(text.getBytes(StandardCharsets.UTF_8));
    }
    @SneakyThrows
    public static String encode(byte[] textBytes) {
        return Base64.getEncoder().encodeToString(textBytes);
    }
    @SneakyThrows
    public static String encode(Object o) {
        return encode(objectMapper.writeValueAsBytes(o));
    }

    @SneakyThrows
    public static OctetSequenceKey generateEncryptionKey() {
        return EncryptionKeyGenerator.generateEncryptionKey();
    }
    @SneakyThrows
    public static OctetSequenceKey generateEncryptionKey(String customKeyId) {
        return new OctetSequenceKey.Builder(EncryptionKeyGenerator.generateEncryptionKey())
                .keyID(customKeyId)
                .build();
    }
    @SneakyThrows
    public static OctetSequenceKey generateMasterEncryptionKey() {
        return new OctetSequenceKey.Builder(EncryptionKeyGenerator.generateEncryptionKey())
                .keyID(Uuids.timeBased().toString())
                .build();
    }

    // generate MEK with a specific timestamp
    @SneakyThrows
    public static OctetSequenceKey generateMasterEncryptionKey(long timestamp) {
        return new OctetSequenceKey.Builder(EncryptionKeyGenerator.generateEncryptionKey())
                .keyID(Uuids.startOf(timestamp).toString())
                .build();
    }
}
