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
import com.nvidia.ess.encryption.config.properties.DefaultKeyProperties;
import com.nvidia.ess.encryption.crypto.key.BaseEncryptionKeyService;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import reactor.core.publisher.Mono;

/**
 * Backwards compatible implementation. To be used while any secrets encrypted using the old global
 * key (default key) still exist. BaseEncryptionKeyService -> CompatibleEncryptionKeyService
 */
@Slf4j
public class CompatibleEncryptionKeyService extends BaseEncryptionKeyService {

    @Setter(onMethod_ = {@Autowired})
    protected DefaultKeyProperties defaultKeyProperties;

    @Override
    public Mono<OctetSequenceKey> getDecryptionKey(String namespace, String kid) {
        if (defaultKeyProperties.getParsedAllDefaultKeys().containsKey(kid)) {
            return Mono.just(defaultKeyProperties.getParsedAllDefaultKeys().get(kid));
        }

        return super.getDecryptionKey(namespace, kid);
    }
}
