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
import com.nvidia.apikeys.services.KeysV2Service;
import com.nvidia.apikeys.vo.IntrospectionKeyVo;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Duration;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class IntrospectionKeyOwnerValidator {

    public static final Duration KEY_OWNER_CACHE_DURATION = Duration.ofMinutes(1);

    private final KeysV2Service keysV2Service;
    private final LoadingCache<OwnerScope, KeyOwnerStatus> ownerStatusCache;

    /**
     * The purpose of this record is to make a composite key for the cache.
     */
    private record OwnerScope(
            String ncaId, KeyOwnerType type, String id, String issuerServiceId) {

    }

    public IntrospectionKeyOwnerValidator(KeysV2Service keysV2Service) {
        this.keysV2Service = keysV2Service;

        ownerStatusCache = CacheBuilder.newBuilder()
                .expireAfterWrite(KEY_OWNER_CACHE_DURATION)
                .maximumSize(1000)
                .build(CacheLoader.from(this::getUpToDateOwnerStatus));
    }

    private KeyOwnerStatus getUpToDateOwnerStatus(OwnerScope owner) {
        return keysV2Service.getEffectiveOwnerStatus(
                owner.ncaId, owner.type, owner.id, owner.issuerServiceId);
    }

    public void assertKeyOwnerValid(IntrospectionKeyVo introspectionKey) {
        KeyVo key = introspectionKey.key();
        if (key.getOwnerType() == null || key.getOwnerId() == null) {
            throw new BadRequestException("Invalid key owner");
        }
        OwnerScope owner = new OwnerScope(introspectionKey.ncaId(), key.getOwnerType(),
                                          key.getOwnerId(), key.getIssuerServiceId());

        KeyOwnerStatus ownerStatus;
        try {
            ownerStatus = ownerStatusCache.get(owner);
        } catch (Exception e) {
            log.error("failed to retrieve key-owner-id:'{}' of key-owner-type:'{}' in nca-id:'{}'"
                      + " via cache", owner.id, owner.type, owner.ncaId, e);
            throw new IllegalStateException("Failed to retrieve key owner details", e);
        }

        if (ownerStatus != KeyOwnerStatus.ACTIVE) {
            // for introspection, we pretend key just does not exist
            throw new NotFoundException("Key not found.");
        }
    }

    public void invalidateCaches() {
        ownerStatusCache.invalidateAll();
    }

}
