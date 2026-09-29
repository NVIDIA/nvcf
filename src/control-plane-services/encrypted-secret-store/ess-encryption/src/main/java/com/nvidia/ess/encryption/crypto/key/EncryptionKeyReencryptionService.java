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

import com.nvidia.ess.encryption.crypto.key.predicate.EncryptionKeyPredicate;
import com.nvidia.ess.encryption.exceptions.EncryptionException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import reactor.core.publisher.Mono;

public interface EncryptionKeyReencryptionService {

    /**
     * Attempt to re-encrypt all encryption keys. If predicate fails, continues to next encryption
     * key. Skips failed re-encryption and continues to next encryption key. Failure will be
     * detected through metrics instead
     *
     * @param predicate predicate that determines if encryption key should be re-encrypted
     * @return number of re-encrypted encryption keys
     * @throws MissingMasterKeyException  if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException        generic encryption exception related to parsing and
     *                                    serialization
     */
    Mono<Integer> reencryptAllEncryptionKeys(EncryptionKeyPredicate predicate);
}
