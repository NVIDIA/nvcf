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
import com.nvidia.ess.encryption.exceptions.MissingKeyException;
import com.nvidia.ess.encryption.exceptions.MissingMasterKeyException;
import com.nvidia.ess.encryption.exceptions.OptimisticLockingException;
import reactor.core.publisher.Mono;

public interface EncryptionKeyRotationService {

    /**
     * Rotate encryption key in a namespace if predicate passes. Previous key should still be
     * available for decryption. Short-circuits if predicate fails
     *
     * @param namespace namespace for which to rotate encryption key
     * @param predicate predicate that determines if encryption key should be rotated
     * @return true if encryption key passed predicate and rotated, false if predicate fails
     * @throws MissingKeyException        if no encryption keys exist in {@code namespace}
     * @throws OptimisticLockingException if another process rotated the same encryption key in
     *                                    {@code namespace}
     * @throws MissingMasterKeyException  if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException        generic encryption exception related to parsing and
     *                                    serialization
     */
    Mono<Boolean> rotateEncryptionKey(String namespace, EncryptionKeyPredicate predicate);


    /**
     * Attempt to rotate all encryption keys. Previous key should still be available for decryption.
     * If predicate fails, continues to next encryption key.
     *
     * @param predicate predicate that determines if encryption key should be rotated
     * @return number of rotated encryption keys
     * @throws OptimisticLockingException if another process rotated the same encryption key in
     *                                    {@code namespace}
     * @throws MissingMasterKeyException  if MEK used to encrypt existing encryption key is missing
     * @throws EncryptionException        generic encryption exception related to parsing and
     *                                    serialization
     */
    Mono<Integer> rotateAllEncryptionKeys(EncryptionKeyPredicate predicate);

}
