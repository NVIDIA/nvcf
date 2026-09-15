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

import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.KEY_OWNER;
import static com.nvidia.apikeys.services.ActionRecorder.VALUE_NOT_APPLICABLE;

import com.nvidia.apikeys.converters.KeyOwnerVoBuilder;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.services.ActionRecorder.Action;
import com.nvidia.apikeys.utils.TracingUtils;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.boot.exceptions.NotFoundException;
import java.time.Instant;
import java.util.Optional;
import java.util.Set;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class KeyOwnerService {

    private final KeyOwnerVoBuilder keyOwnerVoBuilder;
    private final KeysDao dao;
    private final ActionRecorder actionRecorder;
    private final TracingUtils tracingUtils;

    public KeyOwnerVo loadKeyOwnerByTypeAndIdOrCreateNew(KeyOwnerType type, String ownerId) {
        Optional<KeyOwnerVo> keyOwnerVo = dao.getKeyOwner(type, ownerId);
        boolean isNewOwner = keyOwnerVo.isEmpty();

        if (isNewOwner) {
            keyOwnerVo = Optional.of(keyOwnerVoBuilder.getNewKeyOwnerVo(type, ownerId));
        }

        tracingUtils.addKeyOwnerTags(keyOwnerVo.get(), isNewOwner);
        return keyOwnerVo.get();
    }

    public KeyOwnerVo loadExistingKeyOwner(KeyOwnerType type, String ownerId) {
        Optional<KeyOwnerVo> keyOwner = dao.getKeyOwner(type, ownerId);
        if (keyOwner.isEmpty()) {
            throw new NotFoundException("Key owner not found");
        }

        tracingUtils.addKeyOwnerTags(keyOwner.get(), false);
        return keyOwner.get();
    }

    public KeyOwnerVo updateKeyOwner(
            KeyOwnerVo keyOwnerVo, KeyOwnerStatus status, Instant statusUpdatedAt) {
        KeyOwnerVo updatedVo = keyOwnerVo.toBuilder()
                .ownerStatus(status)
                .ownerStatusUpdatedAt(statusUpdatedAt)
                .build();

        actionRecorder.record(KEY_OWNER, VALUE_NOT_APPLICABLE, keyOwnerVo.getOwnerType().name(),
                              keyOwnerVo.getOwnerId(), statusToAction(status),
                              VALUE_NOT_APPLICABLE, Set.of());

        return dao.save(updatedVo);
    }

    private Action statusToAction(KeyOwnerStatus status) {
        switch (status) {
            case ACTIVE -> {
                return Action.ACTIVE;
            }
            case SUSPENDED -> {
                return Action.SUSPENDED;
            }
            default -> {
                return Action.NOT_APPLICABLE;
            }
        }
    }

}
