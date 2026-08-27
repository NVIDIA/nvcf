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

import static com.nvidia.apikeys.services.ActionRecorder.Action.CREATE;
import static com.nvidia.apikeys.services.ActionRecorder.Action.DELETE;
import static com.nvidia.apikeys.services.ActionRecorder.Action.SUSPEND;
import static com.nvidia.apikeys.services.ActionRecorder.Action.UPDATE;
import static com.nvidia.apikeys.services.ActionRecorder.ResourceType.API_KEY;
import static com.nvidia.apikeys.vo.KeyStatus.ACTIVE;
import static com.nvidia.apikeys.vo.KeyStatus.SUSPENDED;

import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.persistance.dao.RowUpdateLockDao;
import com.nvidia.apikeys.persistance.models.KeyModel;
import com.nvidia.apikeys.utils.TracingUtils;
import com.nvidia.apikeys.vo.CreateKeyRequestVo;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.SavedKeyVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.apikeys.vo.SuspendKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import com.nvidia.boot.exceptions.TooManyRequestsException;
import java.time.Clock;
import java.time.Instant;
import java.util.List;
import java.util.Optional;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class KeysService {

    public static final String UPDATE_LOCK_KEY_PREFIX = "authz-for-key-hash-";
    public static final String ERROR_SLOW_DOWN = "Slow down.";


    private final MillisecondPrecisionClock clock;
    private final KeysDao keysDao;
    private final RowUpdateLockDao rowUpdateLockDao;
    private final ActionRecorder actionRecorder;
    private final TracingUtils tracingUtils;


    public CreateKeyRequestVo createKey(CreateKeyRequestVo request) {
        KeyVo key = request.getKey();
        KeyOwnerVo owner = request.getKeyOwner();
        ServiceVo service = request.getService();

        Instant now = clock.instant();

        // Obtain lock for authorizations updates
        lockForAuthorizationUpdates(key, service, now);

        keysDao.save(key, owner);

        actionRecorder.record(API_KEY, key.getKeyId(), owner.getOwnerType().name(),
                              owner.getOwnerId(), CREATE, key.getIssuerServiceId(),
                              key.getAudienceServiceIds());

        tracingUtils.addKeyTags(key);
        return request;
    }

    public KeyVo updateKey(UpdateKeyRequestVo request) {
        KeyVo key = request.getKey();
        KeyOwnerVo owner = request.getKeyOwner();
        ServiceVo service = request.getService();

        Instant now = clock.instant();

        if (request.isAuthorizationsUpdated()) {
            // Obtain lock for authorizations updates
            lockForAuthorizationUpdates(key, service, now);
        }

        // write key record
        SavedKeyVo savedKeyVo = keysDao.save(key, owner);

        actionRecorder.record(API_KEY, key.getKeyId(), owner.getOwnerType().name(),
                              owner.getOwnerId(), UPDATE, key.getIssuerServiceId(),
                              key.getAudienceServiceIds());

        tracingUtils.addKeyTags(key);
        return savedKeyVo.getKeyVo();
    }

    private void lockForAuthorizationUpdates(KeyVo key, ServiceVo service, Instant now) {
        boolean lockAcquired = rowUpdateLockDao.lock(
                KeyModel.TABLE_NAME, getLockRecordKey(key.getKeyHash()),
                now, service.getMinAuthzUpdateIntervalSeconds());

        if (!lockAcquired) {
            throw new TooManyRequestsException(ERROR_SLOW_DOWN);
        }
    }

    public List<KeyByOwnerAndServiceVo> suspendKeys(SuspendKeysRequestVo request) {
        KeyOwnerVo keyOwner = request.getKeyOwner();
        String serviceId = request.getService().getServiceId();

        var allKeys = keysDao.list(
                keyOwner.getOwnerType(), keyOwner.getOwnerId(), serviceId);

        return allKeys.stream()
                .map(key -> key.getKeyStatus() != ACTIVE ? key : suspendKey(key, keyOwner))
                .toList();
    }

    private KeyByOwnerAndServiceVo suspendKey(KeyByOwnerAndServiceVo key, KeyOwnerVo keyOwner) {
        Optional<KeyVo> keyByHash = keysDao.getKeyByHash(key.getKeyHash());
        if (keyByHash.isEmpty()) {
            log.error("failed to suspend api-key-id:'{}' cannot load", key.getKeyId());
        } else {
            KeyVo updatedKeyVo = keyByHash.get().toBuilder()
                    .keyStatus(SUSPENDED)
                    .build();

            SavedKeyVo savedKeyVo = keysDao.save(updatedKeyVo, keyOwner);
            key = savedKeyVo.getKeyByOwnerAndServiceVo();

            actionRecorder.record(API_KEY, key.getKeyId(), keyOwner.getOwnerType().name(),
                                  keyOwner.getOwnerId(), SUSPEND, key.getIssuerServiceId(),
                                  key.getAudienceServiceIds());
        }
        return key;
    }

    public void deleteKeyById(DeleteKeyRequestVo request) {
        KeyOwnerVo keyOwner = request.getKeyOwner();
        KeyByOwnerAndServiceVo key = request.getKey();

        keysDao.deleteKey(key);
        actionRecorder.record(API_KEY, key.getKeyId(), keyOwner.getOwnerType().name(),
                              keyOwner.getOwnerId(), DELETE, key.getIssuerServiceId(),
                              key.getAudienceServiceIds());
    }



    public List<KeyByOwnerAndServiceVo> listKeys(ListKeysRequestVo request) {
        KeyOwnerVo keyOwner = request.getKeyOwner();
        String serviceId = request.getService().getServiceId();

        return keysDao.list(
                keyOwner.getOwnerType(), keyOwner.getOwnerId(), serviceId);
    }

    public void deleteKeys(KeyOwnerVo keyOwnerVo) {
        listKeys(keyOwnerVo).stream()
                .forEach(keysDao::deleteKey);
    }

    public List<KeyByOwnerAndServiceVo> listKeys(KeyOwnerVo keyOwner) {
        return keysDao.list(keyOwner.getOwnerType(), keyOwner.getOwnerId());
    }

    private static String getLockRecordKey(String keyHash) {
        return UPDATE_LOCK_KEY_PREFIX + keyHash;
    }

}
