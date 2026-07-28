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

package com.nvidia.apikeys.facade;

import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateKeyStatusRequest;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.validators.ServiceAdminRequestValidator;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.SuspendKeysRequestVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class ServiceAdminFacade {

    private final ServiceAdminRequestValidator serviceAdminRequestValidator;
    private final KeysService keysService;
    private final KeyResponseBuilder keyResponseBuilder;

    public KeyDto getKeyById(KeyOwnerType keyOwnerType, String keyOwnerId, String keyId) {
        KeyVo keyVo = serviceAdminRequestValidator.validateGetKey(
                keyOwnerType, keyOwnerId, keyId);

        return keyResponseBuilder.toDto(keyVo);
    }

    public KeyDto updateKeyStatus(
            KeyOwnerType keyOwnerType, String keyOwnerId, String keyId,
            UpdateKeyStatusRequest request) {
        UpdateKeyRequestVo requestVo = serviceAdminRequestValidator
                .validateUpdateKeyStatus(keyOwnerType, keyOwnerId, keyId, request);

        KeyVo key = keysService.updateKey(requestVo);

        return keyResponseBuilder.toDto(key);
    }

    public ListKeysResponse listApiKeys(KeyOwnerType keyOwnerType, String keyOwnerId) {
        ListKeysRequestVo request = serviceAdminRequestValidator.validateListKeysRequest(
                keyOwnerType, keyOwnerId);

        List<KeyByOwnerAndServiceVo> keys = keysService.listKeys(request);

        return keyResponseBuilder.toListResponse(keys);
    }

    public ListKeysResponse suspendKeys(KeyOwnerType keyOwnerType, String keyOwnerId) {
        SuspendKeysRequestVo request = serviceAdminRequestValidator.validateSuspendKeysRequest(
                keyOwnerType, keyOwnerId);

        List<KeyByOwnerAndServiceVo> keys = keysService.suspendKeys(request);

        return keyResponseBuilder.toListResponse(keys);
    }

    public void deleteKeyById(KeyOwnerType keyOwnerType, String keyOwnerId, String keyId) {
        DeleteKeyRequestVo request = serviceAdminRequestValidator.validateDeleteKeyRequest(
                keyOwnerType, keyOwnerId, keyId);

        keysService.deleteKeyById(request);
    }
}
