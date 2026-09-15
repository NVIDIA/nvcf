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

import com.nvidia.apikeys.caching.CallerServiceResolver;
import com.nvidia.apikeys.converters.KeyResponseBuilder;
import com.nvidia.apikeys.dto.keys.CreateKeyRequest;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.services.ActorResolver;
import com.nvidia.apikeys.services.ActorToValidatedKeyOwnerResolver;
import com.nvidia.apikeys.services.KeysService;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.validators.KeysOperationByOwnerValidator;
import com.nvidia.apikeys.vo.CreateKeyRequestVo;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import java.util.List;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class UserOwnedKeysFacade {

    private final ActorResolver actorResolver;
    private final CallerServiceResolver callerServiceResolver;
    private final ActorToValidatedKeyOwnerResolver actorToValidatedKeyOwnerResolver;
    private final KeysOperationByOwnerValidator validator;
    private final KeysService keysService;
    private final KeyResponseBuilder keyResponseBuilder;
    private final ValidatingKeyLoader validatingKeyLoader;

    public KeyDto createApiKey(CreateKeyRequest request) {
        String actorId = actorResolver.getValidatedActorId();
        ServiceVo callerService = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwnerVo = actorToValidatedKeyOwnerResolver.getUserKeyOwnerVo(
                callerService, actorId);
        CreateKeyRequestVo createKeyRequestVo = validator.validateCreateKeyRequest(
                callerService, keyOwnerVo, request);

        CreateKeyRequestVo key = keysService.createKey(createKeyRequestVo);

        return keyResponseBuilder.toDto(key.getKey(), key.getGeneratedKeyVo());
    }

    public ListKeysResponse listApiKeys() {
        String actorId = actorResolver.getValidatedActorId();
        ServiceVo callerService = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwnerVo = actorToValidatedKeyOwnerResolver.getUserKeyOwnerVo(
                callerService, actorId);
        ListKeysRequestVo listKeysRequest = validator.validateListKeysRequest(
                callerService, keyOwnerVo);

        List<KeyByOwnerAndServiceVo> keys = keysService.listKeys(listKeysRequest);

        return keyResponseBuilder.toListResponse(keys);
    }

    public KeyDto getKeyById(String keyId) {
        String actorId = actorResolver.getValidatedActorId();
        ServiceVo callerService = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwnerVo = actorToValidatedKeyOwnerResolver.getUserKeyOwnerVo(
                callerService, actorId);
        KeyVo keyVo = validatingKeyLoader.loadKeyVo(keyOwnerVo, callerService.getServiceId(), keyId);

        return keyResponseBuilder.toDto(keyVo);
    }

    public void deleteKeyById(String keyId) {
        String actorId = actorResolver.getValidatedActorId();
        ServiceVo callerService = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwnerVo = actorToValidatedKeyOwnerResolver.getUserKeyOwnerVo(
                callerService, actorId);
        DeleteKeyRequestVo deleteKeyRequest = validator.validateDeleteKeyRequest(
                callerService, keyOwnerVo, keyId);

        keysService.deleteKeyById(deleteKeyRequest);
    }

    public KeyDto updateKeyAuthorizations(String keyId, UpdateAuthorizationsRequest request) {
        String actorId = actorResolver.getValidatedActorId();
        ServiceVo callerService = callerServiceResolver.getCallerService();
        KeyOwnerVo keyOwnerVo = actorToValidatedKeyOwnerResolver.getUserKeyOwnerVo(
                callerService, actorId);
        UpdateKeyRequestVo requestVo = validator.validateUpdateKeyRequest(
                callerService, keyOwnerVo, keyId, request);

        KeyVo key = keysService.updateKey(requestVo);

        return keyResponseBuilder.toDto(key);
    }
}
