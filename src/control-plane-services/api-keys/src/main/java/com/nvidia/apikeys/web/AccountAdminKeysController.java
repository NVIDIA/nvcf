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

package com.nvidia.apikeys.web;

import static com.nvidia.apikeys.facade.KeyManagementActor.ACCOUNT_ADMIN;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.apikeys.dto.keysV2.CreateKeyRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerStatusesResponse;
import com.nvidia.apikeys.dto.keysV2.KeyResponse;
import com.nvidia.apikeys.dto.keysV2.ListKeysResponse;
import com.nvidia.apikeys.dto.keysV2.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateKeyStatusRequest;
import com.nvidia.apikeys.dto.keysV2.UpdateOwnerStatusForAccountRequest;
import com.nvidia.apikeys.facade.KeyManagementFacade;
import com.nvidia.apikeys.vo.KeyStatus;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

/**
 * Account admin endpoints. The account comes from the route. Admins manage a suspended user's
 * keys; they cannot read cross-account statuses.
 */
@RestController
@RequiredArgsConstructor
@RequestMapping(path = "/v2/api-keys/account-admin", produces = APPLICATION_JSON_VALUE)
public class AccountAdminKeysController {

    private static final String NCA_ID = "nca-id";
    private static final String SERVICE_ID = "service-id";
    private static final String USER_ID = "user-id";
    private static final String KEY_ID = "key-id";

    private final KeyManagementFacade facade;

    @Operation(summary = "Create api key", description = "Creates a new api key for the user.")
    @PostMapping(path = "/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys",
            consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyResponse createKey(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @RequestBody CreateKeyRequest request) {
        return facade.createKey(ACCOUNT_ADMIN, ncaId, serviceId, userId, request);
    }

    @Operation(summary = "List api keys", description = "Lists the user's keys in the account "
            + "and issuer service.")
    @GetMapping("/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys")
    @ResponseStatus(HttpStatus.OK)
    public ListKeysResponse listScopedKeys(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId) {
        return facade.listKeys(ACCOUNT_ADMIN, ncaId, serviceId, userId);
    }

    @Operation(summary = "List api keys", description = "Pages every key in the account.")
    @GetMapping("/accounts/{nca-id}/keys")
    @ResponseStatus(HttpStatus.OK)
    public ListKeysResponse listAccountKeys(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @RequestParam(name = "user-id", required = false) String userId,
            @RequestParam(name = "service-id", required = false) String serviceId,
            @RequestParam(name = "status", required = false) KeyStatus status,
            @RequestParam(name = "limit", required = false) Integer limit,
            @RequestParam(name = "cursor", required = false) String cursor) {
        return facade.listKeys(ACCOUNT_ADMIN, ncaId, serviceId, userId, status, limit, cursor);
    }

    @Operation(summary = "Get api key", description = "Retrieves one key by id.")
    @GetMapping("/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys/{key-id}")
    @ResponseStatus(HttpStatus.OK)
    public KeyResponse getKey(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {
        return facade.getKey(ACCOUNT_ADMIN, ncaId, serviceId, userId, keyId);
    }

    @Operation(summary = "Update api key status", description = "Sets the key status.")
    @PutMapping(
            path = "/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys/{key-id}/status",
            consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyResponse updateKeyStatus(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId,
            @Valid @RequestBody UpdateKeyStatusRequest request) {
        return facade.updateKeyStatus(
                ACCOUNT_ADMIN, ncaId, serviceId, userId, keyId, request.getStatus());
    }

    @Operation(summary = "Update api key authorizations",
               description = "Replaces the key's authorization document.")
    @PutMapping(
            path = "/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys/{key-id}/authorizations",
            consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyResponse updateKeyAuthorizations(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId,
            @RequestBody UpdateAuthorizationsRequest request) {
        return facade.updateKeyAuthorizations(
                ACCOUNT_ADMIN, ncaId, serviceId, userId, keyId, request);
    }

    @Operation(summary = "Delete api key", description = "Deletes one key by id.")
    @DeleteMapping("/accounts/{nca-id}/services/{service-id}/users/{user-id}/keys/{key-id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteKey(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = SERVICE_ID, required = true,
                    description = "Unique identifier of the issuer service.")
            @PathVariable(SERVICE_ID) String serviceId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {
        facade.deleteKey(ACCOUNT_ADMIN, ncaId, serviceId, userId, keyId);
    }

    @Operation(summary = "Get owner status", description = "The user's status in this account, "
            + "as exactly one entry.")
    @GetMapping("/accounts/{nca-id}/users/{user-id}/statuses")
    @ResponseStatus(HttpStatus.OK)
    public KeyOwnerStatusesResponse getAccountOwnerStatuses(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId) {
        return facade.getAccountOwnerStatuses(ncaId, userId);
    }

    @Operation(summary = "Update owner status", description = "Sets the user's status for the "
            + "account.")
    @PutMapping(
            path = "/accounts/{nca-id}/users/{user-id}/status",
            consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyOwnerStatusResponse updateAccountStatus(
            @Parameter(name = NCA_ID, required = true,
                    description = "Unique identifier of the account.")
            @PathVariable(NCA_ID) String ncaId,
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId,
            @Valid @RequestBody UpdateOwnerStatusForAccountRequest request) {
        return facade.updateAccountStatus(ACCOUNT_ADMIN, ncaId, userId, request);
    }
}
