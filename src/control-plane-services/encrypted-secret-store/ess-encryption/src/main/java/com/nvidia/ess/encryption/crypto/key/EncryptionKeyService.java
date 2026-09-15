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
package com.nvidia.ess.encryption.crypto.key;

import com.nimbusds.jose.jwk.OctetSequenceKey;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import reactor.core.publisher.Mono;

public interface EncryptionKeyService {

    /**
     * Get namespace specific encryption key. If key does not exist, return created encryption key
     *
     * @param namespace namespace for which to get the encryption key
     * @return existing or, if it does not exist, generated encryption key
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing and
     *                                   serialization
     */
    Mono<OctetSequenceKey> getEncryptionKey(String namespace);

    /**
     * Get decryption key matching KeyId
     *
     * @param namespace namespace for which to get the decryption key
     * @param kid       KeyId used to match decryption key
     * @return decryption key if namespace keys exists and if kid matches one of them
     * @throws MissingKeyException       if encryption key corresponding to {@code kid} is missing
     *                                   amongst {@code namespace} encryption keys
     * @throws MissingMasterKeyException if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException       generic encryption exception related to parsing and
     *                                   serialization
     */
    Mono<OctetSequenceKey> getDecryptionKey(String namespace, String kid);
}
