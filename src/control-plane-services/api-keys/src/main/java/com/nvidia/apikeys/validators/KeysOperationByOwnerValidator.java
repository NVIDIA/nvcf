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

import com.nvidia.apikeys.config.NakProperties;
import com.nvidia.apikeys.converters.ValidatingAuthorizationsConverter;
import com.nvidia.apikeys.dto.keys.CreateKeyRequest;
import com.nvidia.apikeys.dto.keys.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.persistance.dao.KeysDao;
import com.nvidia.apikeys.services.CredentialService;
import com.nvidia.apikeys.services.MillisecondPrecisionClock;
import com.nvidia.apikeys.services.ValidatingKeyLoader;
import com.nvidia.apikeys.vo.CreateKeyRequestVo;
import com.nvidia.apikeys.vo.DeleteKeyRequestVo;
import com.nvidia.apikeys.vo.GeneratedKeyVo;
import com.nvidia.apikeys.vo.KeyByOwnerAndServiceVo;
import com.nvidia.apikeys.vo.KeyOwnerVo;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeyVo;
import com.nvidia.apikeys.vo.ListKeysRequestVo;
import com.nvidia.apikeys.vo.ServiceVo;
import com.nvidia.apikeys.vo.UpdateKeyRequestVo;
import com.nvidia.boot.exceptions.BadRequestException;
import java.time.temporal.ChronoUnit;
import java.util.List;
import java.util.Set;
import lombok.RequiredArgsConstructor;
import org.apache.commons.collections4.CollectionUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class KeysOperationByOwnerValidator {

    public static final String REACHED_MAXIMUM_KEYS_IN_SERVICE = "Actor reached maximum allowed number of keys in service";

    private final NakProperties nakProperties;
    private final CredentialService credentialService;
    private final KeysDao keysDao;
    private final KeyRequestValidator keyRequestValidator;
    private final ValidatingAuthorizationsConverter authorizationsConverter;
    private final MillisecondPrecisionClock clock;
    private final ValidatingKeyLoader validatingKeyLoader;

    public ListKeysRequestVo validateListKeysRequest(ServiceVo service, KeyOwnerVo keyOwnerVo) {
        return ListKeysRequestVo.builder()
                .keyOwner(keyOwnerVo)
                .service(service)
                .build();
    }

    public CreateKeyRequestVo validateCreateKeyRequest(
            ServiceVo service, KeyOwnerVo keyOwnerVo, CreateKeyRequest createKeyRequest) {

        keyRequestValidator.assertDescriptionValid(createKeyRequest.getDescription());
        keyRequestValidator.assertExpirationDateValid(service, createKeyRequest.getExpiresAt());

        Set<String> audienceServiceIds = keyRequestValidator.getValidAudienceServiceIds(
                service, createKeyRequest.getAudienceServiceIds());

        List<KeyByOwnerAndServiceVo> existingActorKeys = keysDao.list(
                keyOwnerVo.getOwnerType(), keyOwnerVo.getOwnerId(), service.getServiceId());

        assertNotReachedLimitKeysPerService(service, keyOwnerVo, existingActorKeys);

        GeneratedKeyVo generatedKeyVo = credentialService.generateApiKey();

        String authorizations = authorizationsConverter.readValidAuthorizations(
                service, createKeyRequest.getAuthorizations());

        KeyVo keyVo = KeyVo.builder()
                .keyId(generatedKeyVo.getKeyId())
                .keyHash(generatedKeyVo.getKeyHash())
                .audienceServiceIds(audienceServiceIds)
                .issuerServiceId(service.getServiceId())
                .keyStatus(KeyStatus.ACTIVE)
                .apiKeySuffix(generatedKeyVo.getKeySuffix())
                .description(createKeyRequest.getDescription())
                .createdAt(clock.instant().truncatedTo(ChronoUnit.MILLIS))
                .expiresAt(createKeyRequest.getExpiresAt())
                .deletesAt(createKeyRequest.getExpiresAt()
                                   .plus(nakProperties.getKeepAfterExpiredDuration()))
                .authorizations(authorizations)
                .ownerType(keyOwnerVo.getOwnerType())
                .ownerId(keyOwnerVo.getOwnerId())
                .build();

        return CreateKeyRequestVo.builder()
                .service(service)
                .keyOwner(keyOwnerVo)
                .generatedKeyVo(generatedKeyVo)
                .key(keyVo)
                .build();
    }

    public DeleteKeyRequestVo validateDeleteKeyRequest(
            ServiceVo service, KeyOwnerVo keyOwnerVo, String keyId) {

        KeyByOwnerAndServiceVo key = validatingKeyLoader.loadKeyByOwnerAndServiceVo(
                keyOwnerVo, service.getServiceId(), keyId);

        return DeleteKeyRequestVo.builder()
                .key(key)
                .keyOwner(keyOwnerVo)
                .service(service)
                .build();
    }

    public UpdateKeyRequestVo validateUpdateKeyRequest(
            ServiceVo service, KeyOwnerVo keyOwnerVo, String keyId,
            UpdateAuthorizationsRequest request) {
        String authorizations = authorizationsConverter.readValidAuthorizations(
                service, request.getAuthorizations());

        KeyVo keyBeforeUpdate = validatingKeyLoader.loadKeyVo(
                keyOwnerVo, service.getServiceId(), keyId);

        keyRequestValidator.assertKeyActive(keyBeforeUpdate);

        var key = keyBeforeUpdate
                .toBuilder()
                .authorizations(authorizations)
                .build();

        return UpdateKeyRequestVo.builder()
                .key(key)
                .authorizationsUpdated(true)
                .service(service)
                .keyOwner(keyOwnerVo)
                .build();
    }

    private void assertNotReachedLimitKeysPerService(
            ServiceVo service, KeyOwnerVo keyOwnerVo,
            List<KeyByOwnerAndServiceVo> existingActorKeys) {
        switch (keyOwnerVo.getOwnerType()) {
            case USER -> {
                if (CollectionUtils.size(existingActorKeys) >= service.getMaxApiKeysPerUser()) {
                    throw new BadRequestException(REACHED_MAXIMUM_KEYS_IN_SERVICE);
                }
            }
            default -> throw new IllegalStateException(
                    "Unexpected value: " + keyOwnerVo.getOwnerType());
        }

    }
}
