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

import static com.nvidia.apikeys.vo.KeyOwnerType.USER;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateKeyStatusRequest;
import com.nvidia.apikeys.facade.ServiceAdminFacade;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import lombok.RequiredArgsConstructor;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE, path = "/v1/service-admin/users")
public class UserAdminController {

    private static final String USER_ID = "user-id";
    private static final String KEY_ID = "key-id";

    private final ServiceAdminFacade facade;

    @Operation(summary = "Get key by id", description = "Retrieve key details by id")
    @GetMapping("/{user-id}/keys/{key-id}")
    public KeyDto getKeyById(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose key is being retrieved.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {
        return facade.getKeyById(USER, userId, keyId);
    }

    @Operation(summary = "Delete key by id", description = "Delete key by id")
    @DeleteMapping("/{user-id}/keys/{key-id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteKeyById(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose key is being deleted.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {

        facade.deleteKeyById(USER, userId, keyId);
    }

    @Operation(summary = "Update key status", description = "Update key status")
    @PutMapping(path = "/{user-id}/keys/{key-id}/status", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyDto updateKeyStatus(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose key is being retrieved.")
            @PathVariable(USER_ID) String userId,
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId,
            @RequestBody UpdateKeyStatusRequest request) {
        return facade.updateKeyStatus(USER, userId, keyId, request);
    }

    @Operation(summary = "Suspend keys", description = "Suspend all user keys in service")
    @PutMapping(path = "/{user-id}/suspend", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public ListKeysResponse suspendAllUserKeys(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose keys are being suspended.")
            @PathVariable(USER_ID) String userId) {
        return facade.suspendKeys(USER, userId);
    }

    @Operation(summary = "List api keys", description = "List api keys")
    @GetMapping("/{user-id}/keys")
    @ResponseStatus(HttpStatus.OK)
    public ListKeysResponse listKeys(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose keys are being retrieved.")
            @PathVariable(USER_ID) String userId) {
        return facade.listApiKeys(USER, userId);
    }

}
