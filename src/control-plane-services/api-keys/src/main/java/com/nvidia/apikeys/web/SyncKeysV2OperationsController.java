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

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.apikeys.dto.keysV2.AccountAdminKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.KeyOperationResponse;
import com.nvidia.apikeys.dto.keysV2.KeyOwnerKeysOperationRequest;
import com.nvidia.apikeys.dto.keysV2.PlatformAdminKeysOperationRequest;
import com.nvidia.apikeys.services.SyncKeysV2OperationsService;
import io.swagger.v3.oas.annotations.Operation;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@RequestMapping(path = "/v2/api-keys", produces = APPLICATION_JSON_VALUE, consumes = APPLICATION_JSON_VALUE)
public class SyncKeysV2OperationsController {

    private final SyncKeysV2OperationsService service;

    @Operation(summary = "Synchronously revoke or delete the selected user's keys")
    @PutMapping("/key-owner/users/{userId}/key-operations")
    public KeyOperationResponse operateAsKeyOwner(
            @PathVariable("userId") String userId, @Valid @RequestBody KeyOwnerKeysOperationRequest request) {
        return service.operateAsKeyOwner(userId, request);
    }

    @Operation(summary = "Synchronously revoke or delete selected keys in an account")
    @PutMapping("/account-admin/accounts/{ncaId}/key-operations")
    public KeyOperationResponse operateAsAccountAdmin(
            @PathVariable("ncaId") String ncaId, @Valid @RequestBody AccountAdminKeysOperationRequest request) {
        return service.operateAsAccountAdmin(ncaId, request);
    }

    @Operation(summary = "Synchronously revoke or delete selected keys across accounts")
    @PutMapping("/platform-admin/key-operations")
    public KeyOperationResponse operateAsPlatformAdmin(
            @Valid @RequestBody PlatformAdminKeysOperationRequest request) {
        return service.operateAsPlatformAdmin(request);
    }
}
