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
import com.nvidia.apikeys.dto.keys.KeyLookupRequest;
import com.nvidia.apikeys.dto.keys.KeyOwnerDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateKeyOwnerStatusRequest;
import com.nvidia.apikeys.facade.AdminFacade;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import lombok.RequiredArgsConstructor;
import org.springframework.http.HttpStatus;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequiredArgsConstructor
@RequestMapping(path = "/v1/admin", produces = APPLICATION_JSON_VALUE)
public class AdminController {

    private static final String USER_ID = "user-id";

    private final AdminFacade facade;

    @Operation(summary = "Lookup key owner", description = "Returns details about the key and owner.")
    @PostMapping(path = "/lookup", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyDto lookup(@RequestBody KeyLookupRequest request) {
        return facade.lookup(request);
    }

    @Operation(summary = "List api keys",
            description = "List api keys belonging to a user across all services.")
    @GetMapping("/users/{user-id}/keys")
    public ListKeysResponse getUserKeyById(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose keys are being retrieved.")
            @PathVariable(USER_ID) String userId) {
        return facade.listKeysInAllServices(USER, userId);
    }

    @Operation(summary = "Delete keys", description = "Deletes key by id")
    @DeleteMapping("/users/{user-id}/keys")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteAllUserKeys(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose keys are being deleted.")
            @PathVariable(USER_ID) String userId) {
        facade.deleteUserKeys(USER, userId);
    }

    @Operation(summary = "Get key owner status", description = "Retrieves key owner status")
    @GetMapping("/users/{user-id}")
    public KeyOwnerDto getUserById(
            @Parameter(name = USER_ID, required = true, description = "Unique identifier of the user.")
            @PathVariable(USER_ID) String userId) {
        return facade.getKeyOwner(USER, userId);
    }

    @Operation(summary = "Update key owner status", description = "Update key owner status")
    @PutMapping(path = "/users/{user-id}/status", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyOwnerDto updateUserStatus(
            @Parameter(name = USER_ID, required = true,
                    description = "Unique identifier of the user whose status is being updated.")
            @PathVariable(USER_ID) String userId,
            @RequestBody UpdateKeyOwnerStatusRequest request) {
        return facade.updateKeyOwnerStatus(USER, userId, request);
    }

}
