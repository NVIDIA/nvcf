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
import lombok.extern.slf4j.Slf4j;
import reactor.core.publisher.Mono;

/**
 * TODO remove after rollout.
 * Rollout implementation. Namespace encryption keys to be rolled out slowly using an allowList
 * BaseEncryptionKeyService -> CompatibleEncryptionKeyService -> AllowListEncryptionKeyService
 */
@Slf4j
public class AllowListEncryptionKeyService extends CompatibleEncryptionKeyService {

    @Override
    public Mono<OctetSequenceKey> getEncryptionKey(String namespace) {
        if (inAllowList(namespace)) {
            return super.getEncryptionKey(namespace);
        }

        return getDefaultKey();
    }

    private boolean inAllowList(String namespace) {
        return !encryptionProperties.getRollout().isUseAllowList() ||
                encryptionProperties.getRollout().getAllowList().contains(namespace);
    }

    private Mono<OctetSequenceKey> getDefaultKey() {
        return Mono.just(defaultKeyProperties.getParsedDefaultKey());
    }
}
