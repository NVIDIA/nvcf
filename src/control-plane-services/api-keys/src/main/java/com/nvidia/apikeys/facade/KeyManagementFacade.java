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

import static com.nvidia.apikeys.vo.KeyOwnerType.USER;

import com.nvidia.apikeys.converters.KeyV2ResponseBuilder;
import com.nvidia.apikeys.dto.keysV2.CreateKeyRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.ListKeysResponse;
import com.nvidia.apikeys.dto.keysV2.SettableKeyStatus;
import com.nvidia.apikeys.dto.keysV2.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateOwnerStatusForAccountAndIssuerServiceRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateOwnerStatusForAccountRequest;
import com.nvidia.apikeys.services.KeysV2Service;
import com.nvidia.apikeys.validators.KeyManagementRequestValidator;
import com.nvidia.apikeys.vo.KeyOwnerStatus;
import com.nvidia.apikeys.vo.KeyStatus;
import com.nvidia.apikeys.vo.KeysByScopeFilterVo;
import com.nvidia.apikeys.vo.OwnerAccountStatusVo;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

/**
 * Key management behind the key-owner, account-admin and platform-admin endpoints. The three
 * actors share every operation. The actor selects the route dimensions it must supply and
 * whether a suspended owner is denied: a key owner is denied, administrators are not.
 *
 * <p>The NCA, issuer service and user always come from the route.
 */
@Service
@RequiredArgsConstructor
public class KeyManagementFacade {

    static final String MESG_ACTOR_NOT_ALLOWED = "Operation is not available to this actor";
    static final String MESG_INVALID_CURSOR = "Invalid cursor: '%s'";

    private final KeysV2Service keysService;
    private final KeyManagementRequestValidator validator;
    private final KeyV2ResponseBuilder responseBuilder;

    public KeyResponse createKey(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            CreateKeyRequest request) {
        assertScope(ncaId, serviceId, userId);
        var service = validator.requireService(serviceId);
        var create = validator.validateCreateKeyRequest(service, ncaId, userId, request);

        keysService.createKey(create.key(), service.getMinAuthzUpdateIntervalSeconds(),
                              actor == KeyManagementActor.KEY_OWNER);
        return responseBuilder.toCreateResponse(create.key(), create.generatedKey());
    }

    /**
     * Every key the user has in the issuer service of the account, including legacy keys. The
     * count is bounded by the service key limit per user, so the list is not paged.
     */
    public ListKeysResponse listKeys(
            KeyManagementActor actor, String ncaId, String serviceId, String userId) {
        assertScope(ncaId, serviceId, userId);
        guardOwner(actor, ncaId, serviceId, userId);

        var keys = keysService.listKeys(ncaId, USER, userId, serviceId);
        return responseBuilder.toListResponse(keys, null, null);
    }

    /**
     * Pages keys across the dimensions the caller leaves open. The actor must supply the
     * dimension its route fixes: the user for a key owner and the NCA for an account admin. An
     * omitted filter applies no restriction. Only a platform admin may omit every filter.
     */
    public ListKeysResponse listKeys(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            KeyStatus status, Integer limit, String cursor) {
        switch (actor) {
            case KEY_OWNER -> validator.assertRouteId("userId", userId);
            case ACCOUNT_ADMIN -> validator.assertRouteId("ncaId", ncaId);
            case PLATFORM_ADMIN -> { }
        }
        assertOptionalId("ncaId", ncaId);
        assertOptionalId("serviceId", serviceId);
        assertOptionalId("userId", userId);

        var unfiltered = ncaId == null && serviceId == null && userId == null;
        var resolvedLimit = validator.resolveLimit(limit, unfiltered);
        var filter = KeysByScopeFilterVo.builder()
                .ncaId(ncaId)
                .ownerType(userId == null ? null : USER)
                .ownerId(userId)
                .issuerServiceId(serviceId)
                .keyStatus(status)
                .build();

        try {
            var slice = keysService.listKeys(filter, resolvedLimit, cursor);
            return responseBuilder.toListResponse(slice.keys(), resolvedLimit, slice.cursor());
        } catch (IllegalArgumentException e) {
            throw new BadRequestException(e.getMessage());
        }
    }

    public KeyResponse getKey(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            String keyId) {
        assertKeyRoute(ncaId, serviceId, userId, keyId);
        guardOwner(actor, ncaId, serviceId, userId);

        return keysService.getKeyById(ncaId, USER, userId, serviceId, keyId)
                .map(responseBuilder::toResponse)
                .orElseThrow(() -> new NotFoundException(KeysV2Service.MESG_KEY_NOT_FOUND));
    }

    public KeyResponse updateKeyStatus(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            String keyId, SettableKeyStatus status) {
        assertKeyRoute(ncaId, serviceId, userId, keyId);
        guardOwner(actor, ncaId, serviceId, userId);

        var updated = keysService.updateKeyStatus(
                ncaId, USER, userId, serviceId, keyId, KeyStatus.valueOf(status.name()));
        return responseBuilder.toResponse(updated);
    }

    public KeyResponse updateKeyAuthorizations(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            String keyId, UpdateAuthorizationsRequest request) {
        assertKeyRoute(ncaId, serviceId, userId, keyId);
        var service = validator.requireService(serviceId);
        var authorizations = validator.validateAuthorizations(service, request.getAuthorizations());
        guardOwner(actor, ncaId, serviceId, userId);

        var updated = keysService.updateKeyAuthorizations(
                ncaId, USER, userId, serviceId, keyId, authorizations,
                service.getMinAuthzUpdateIntervalSeconds());
        return responseBuilder.toResponse(updated);
    }

    public void deleteKey(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            String keyId) {
        assertKeyRoute(ncaId, serviceId, userId, keyId);
        guardOwner(actor, ncaId, serviceId, userId);

        keysService.deleteKeyById(ncaId, USER, userId, serviceId, keyId);
    }

    /**
     * The user's status in one account, or in every account known to API Keys when ncaId is
     * null. A single account is always returned as one entry, ACTIVE when nothing is stored.
     */
    public KeyOwnerStatusesResponse getOwnerStatuses(
            KeyManagementActor actor, String ncaId, String userId, Integer limit, String cursor) {
        validator.assertRouteId("userId", userId);
        var accounts = keysService.listOwnerAccountStatuses(USER, userId);

        if (ncaId != null) {
            validator.assertRouteId("ncaId", ncaId);
            var account = accounts.stream()
                    .filter(status -> status.ncaId().equals(ncaId))
                    .findFirst()
                    .orElseGet(() -> OwnerAccountStatusVo.builder()
                            .ncaId(ncaId).status(KeyOwnerStatus.ACTIVE).issuerServices(List.of())
                            .build());
            return responseBuilder.toStatusesResponse(userId, List.of(account), null, null);
        }

        if (actor == KeyManagementActor.ACCOUNT_ADMIN) {
            throw new ForbiddenException(MESG_ACTOR_NOT_ALLOWED);
        }
        var resolvedLimit = validator.resolveLimit(limit, false);
        var after = decodeCursor(cursor);
        var remaining = accounts.stream()
                .filter(status -> after == null || status.ncaId().compareTo(after) > 0)
                .toList();
        var page = remaining.stream().limit(resolvedLimit).toList();
        var nextCursor = remaining.size() > page.size()
                ? encodeCursor(page.getLast().ncaId())
                : null;
        return responseBuilder.toStatusesResponse(userId, page, resolvedLimit, nextCursor);
    }

    public KeyOwnerStatusResponse updateAccountStatus(
            KeyManagementActor actor, String ncaId, String userId,
            UpdateOwnerStatusForAccountRequest request) {
        assertAdmin(actor);
        validator.assertRouteId("ncaId", ncaId);
        validator.assertRouteId("userId", userId);

        var saved = keysService.updateOwnerStatusByAccount(
                ncaId, USER, userId, request.getStatus());
        return responseBuilder.toStatusResponse(saved);
    }

    public KeyOwnerStatusResponse updateIssuerServiceStatus(
            KeyManagementActor actor, String ncaId, String serviceId, String userId,
            UpdateOwnerStatusForAccountAndIssuerServiceRequest request) {
        if (actor != KeyManagementActor.PLATFORM_ADMIN) {
            throw new ForbiddenException(MESG_ACTOR_NOT_ALLOWED);
        }
        assertScope(ncaId, serviceId, userId);

        var saved = keysService.updateOwnerStatusByAccountAndService(
                ncaId, USER, userId, serviceId, request.getStatus());
        return responseBuilder.toStatusResponse(saved);
    }

    private void guardOwner(
            KeyManagementActor actor, String ncaId, String serviceId, String userId) {
        if (actor == KeyManagementActor.KEY_OWNER) {
            keysService.assertOwnerActive(ncaId, USER, userId, serviceId);
        }
    }

    private static void assertAdmin(KeyManagementActor actor) {
        if (actor == KeyManagementActor.KEY_OWNER) {
            throw new ForbiddenException(MESG_ACTOR_NOT_ALLOWED);
        }
    }

    private void assertScope(String ncaId, String serviceId, String userId) {
        validator.assertRouteId("ncaId", ncaId);
        validator.assertRouteId("serviceId", serviceId);
        validator.assertRouteId("userId", userId);
    }

    private void assertKeyRoute(String ncaId, String serviceId, String userId, String keyId) {
        assertScope(ncaId, serviceId, userId);
        validator.assertRouteId("keyId", keyId);
    }

    private void assertOptionalId(String name, String value) {
        if (value != null) {
            validator.assertRouteId(name, value);
        }
    }

    private static String encodeCursor(String ncaId) {
        return Base64.getUrlEncoder().withoutPadding()
                .encodeToString(ncaId.getBytes(StandardCharsets.UTF_8));
    }

    private static String decodeCursor(String cursor) {
        if (StringUtils.isBlank(cursor)) {
            return null;
        }
        try {
            return new String(Base64.getUrlDecoder().decode(cursor), StandardCharsets.UTF_8);
        } catch (IllegalArgumentException e) {
            throw new BadRequestException(MESG_INVALID_CURSOR.formatted(cursor));
        }
    }
}
