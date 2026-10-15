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

package com.nvidia.apikeys.caching;

import com.google.common.cache.CacheBuilder;
import com.google.common.cache.CacheLoader;
import com.google.common.cache.LoadingCache;
import com.nvidia.apikeys.persistance.dao.KeysDao;

import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Duration;
import java.util.Optional;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class IntrospectionKeyOwnerValidator {

    public static final Duration KEY_OWNER_CACHE_DURATION = Duration.ofMinutes(1);

    private final KeysDao keysDao;
    private final LoadingCache<KeyOwnerTypeId, Optional<KeyOwnerVo>> keyOwnerCache;

    /**
     * The purpose of this record is to make a composite key for the cache.
     */
    private record KeyOwnerTypeId(KeyOwnerType type, String id) {

    }

    public IntrospectionKeyOwnerValidator(
            KeysDao keysDao) {

        this.keysDao = keysDao;

        keyOwnerCache = CacheBuilder.newBuilder()
                .expireAfterWrite(KEY_OWNER_CACHE_DURATION)
                .maximumSize(1000)
                .build(CacheLoader.from(this::getUpToDateKeyOwner));
    }

    private Optional<KeyOwnerVo> getUpToDateKeyOwner(KeyOwnerTypeId keyOwner) {
        return keysDao.getKeyOwner(keyOwner.type, keyOwner.id);
    }

    public void assertKeyOwnerValid(KeyVo key) {
        KeyOwnerTypeId owner = new KeyOwnerTypeId(key.getOwnerType(), key.getOwnerId());

        Optional<KeyOwnerVo> keyOwner;
        try {
            keyOwner = keyOwnerCache.get(owner);
        } catch (Exception e) {
            log.error("failed to retrieve key-owner-id:'{}' of key-owner-type:'{}' via cache",
                      owner.id, owner.type, e);
            throw new IllegalStateException("Failed to retrieve key owner details", e);
        }

        if (keyOwner.isEmpty()) {
            throw new BadRequestException("Invalid key owner");
        }

        if (keyOwner.get().getOwnerStatus() != KeyOwnerStatus.ACTIVE) {
            // for introspection, we pretend key just does not exist
            throw new NotFoundException("Key not found.");
        }
    }

    public void invalidateCaches() {
        keyOwnerCache.invalidateAll();
    }

}
