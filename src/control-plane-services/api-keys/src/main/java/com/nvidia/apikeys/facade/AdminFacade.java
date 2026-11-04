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

import com.nvidia.apikeys.converters.KeyOwnerConverter;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.KeyLookupRequest;
import com.nvidia.apikeys.dto.keys.KeyOwnerDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateKeyOwnerStatusRequest;
import com.nvidia.apikeys.services.KeyOwnerService;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.validators.ApiKeyParser;
import com.nvidia.apikeys.vo.KeyOwnerType;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.Clock;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class AdminFacade {

    private final ApiKeyParser apiKeyParser;
    private final KeyResponseBuilder responseBuilder;
    private final KeyOwnerService keyOwnerService;
    private final KeysService keysService;
    private final ValidatingKeyLoader keyLoader;
    private final KeyOwnerConverter keyOwnerConverter;
    private final Clock clock;

    public KeyDto lookup(KeyLookupRequest request) {
        String apiKeyHash = apiKeyParser.rawApiKeyToHash(request.getKey());
        KeyVo key = keyLoader.loadKeyByHash(apiKeyHash);
        return responseBuilder.toLookupDto(key);
    }

    public ListKeysResponse listKeysInAllServices(KeyOwnerType type, String ownerId) {
        KeyOwnerVo keyOwnerVo = keyOwnerService.loadExistingKeyOwner(type, ownerId);

        var keys = keysService.listKeys(keyOwnerVo);

        return responseBuilder.toListResponse(keys);
    }

    public void deleteUserKeys(KeyOwnerType type, String ownerId) {
        KeyOwnerVo keyOwnerVo = keyOwnerService.loadExistingKeyOwner(type, ownerId);

        keysService.deleteKeys(keyOwnerVo);
    }

    public KeyOwnerDto getKeyOwner(KeyOwnerType type, String ownerId) {
        KeyOwnerVo keyOwnerVo = keyOwnerService.loadExistingKeyOwner(type, ownerId);

        return keyOwnerConverter.toDto(keyOwnerVo);
    }

    public KeyOwnerDto updateKeyOwnerStatus(
            KeyOwnerType user, String userId, UpdateKeyOwnerStatusRequest request) {
        KeyOwnerVo keyOwnerVo = keyOwnerService.loadExistingKeyOwner(user, userId);
        if (request == null || request.getStatus() == null) {
            throw new BadRequestException("status is required parameter");
        }

        keyOwnerVo = keyOwnerService.updateKeyOwner(keyOwnerVo, request.getStatus(),
                                                    clock.instant());

        return keyOwnerConverter.toDto(keyOwnerVo);
    }
}
