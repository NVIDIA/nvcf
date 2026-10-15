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

package com.nvidia.apikeys.validators;

import com.nvidia.apikeys.caching.CallerServiceResolver;
import com.nvidia.apikeys.dto.keys.UpdateKeyStatusRequest;
import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.apikeys.vo.SuspendKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class ServiceAdminRequestValidator {

    private final CallerServiceResolver callerServiceResolver;
    private final KeyOwnerService keyOwnerService;
    private final KeyStatusTransitionValidator keyStatusTransitionValidator;
    private final ValidatingKeyLoader validatingKeyLoader;

    public KeyVo validateGetKey(
            KeyOwnerType keyOwnerType, String keyOwnerId, String keyId) {
        ServiceVo service = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                keyOwnerType, keyOwnerId);

        return validatingKeyLoader.loadKeyVo(keyOwner, service.getServiceId(), keyId);
    }

    public UpdateKeyRequestVo validateUpdateKeyStatus(
            KeyOwnerType keyOwnerType, String keyOwnerId, String keyId,
            UpdateKeyStatusRequest request) {

        ServiceVo service = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                keyOwnerType, keyOwnerId);

        KeyVo keyVo = validatingKeyLoader.loadKeyVo(keyOwner, service.getServiceId(), keyId);

        keyStatusTransitionValidator.assertStatusTransitionValid(
                keyVo.getKeyStatus(), request.getStatus());

        KeyVo updatedKeyVo = keyVo.toBuilder()
                .keyStatus(request.getStatus())
                .build();

        return UpdateKeyRequestVo.builder()
                .key(updatedKeyVo)
                .service(service)
                .keyOwner(keyOwner)
                .authorizationsUpdated(false)
                .build();
    }

    public ListKeysRequestVo validateListKeysRequest(KeyOwnerType keyOwnerType, String keyOwnerId) {
        ServiceVo service = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                keyOwnerType, keyOwnerId);

        return ListKeysRequestVo.builder()
                .keyOwner(keyOwner)
                .service(service)
                .build();
    }

    public SuspendKeysRequestVo validateSuspendKeysRequest(
            KeyOwnerType keyOwnerType, String keyOwnerId) {
        ServiceVo service = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                keyOwnerType, keyOwnerId);

        return SuspendKeysRequestVo.builder()
                .keyOwner(keyOwner)
                .service(service)
                .build();
    }

    public DeleteKeyRequestVo validateDeleteKeyRequest(
            KeyOwnerType keyOwnerType, String keyOwnerId, String keyId) {
        ServiceVo service = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwner = keyOwnerService.loadKeyOwnerByTypeAndIdOrCreateNew(
                keyOwnerType, keyOwnerId);

        KeyByOwnerAndServiceVo key = validatingKeyLoader.loadKeyByOwnerAndServiceVo(
                keyOwner, service.getServiceId(), keyId);

        return DeleteKeyRequestVo.builder()
                .keyOwner(keyOwner)
                .service(service)
                .key(key)
                .build();
    }
}
