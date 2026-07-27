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

import com.nvidia.apikeys.dto.keys.CreateKeyRequest;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.dto.keys.ListKeysResponse;
import com.nvidia.apikeys.dto.keys.UpdateAuthorizationsRequest;
import com.nvidia.apikeys.facade.UserOwnedKeysFacade;
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
@RequestMapping(produces = APPLICATION_JSON_VALUE)
public class UserOwnedKeysController {

    public static final String KEY_ID = "key-id";
    private final UserOwnedKeysFacade facade;

    @Operation(summary = "Create api key", description = "Creates new api key.")
    @PostMapping(path = "/v1/keys", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyDto createKey(@RequestBody CreateKeyRequest request) {
        return facade.createApiKey(request);
    }

    @Operation(summary = "List api keys", description = "List api keys")
    @GetMapping("/v1/keys")
    @ResponseStatus(HttpStatus.OK)
    public ListKeysResponse listKeys() {
        return facade.listApiKeys();
    }

    @Operation(summary = "Get key by id", description = "Retrieve key details by id")
    @GetMapping("/v1/keys/{key-id}")
    public KeyDto getKeyById(
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {
        return facade.getKeyById(keyId);
    }

    @Operation(summary = "Delete key by id", description = "Deletes key by id")
    @DeleteMapping("/v1/keys/{key-id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteKeyById(
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId) {
        facade.deleteKeyById(keyId);
    }

    @Operation(summary = "Update key authorizations", description = "Update key authorizations")
    @PutMapping(path = "/v1/keys/{key-id}/authorizations", consumes = APPLICATION_JSON_VALUE)
    @ResponseStatus(HttpStatus.OK)
    public KeyDto updateKeyAuthorizations(
            @Parameter(name = KEY_ID, required = true, description = "Unique identifier of the key.")
            @PathVariable(KEY_ID) String keyId,
            @RequestBody UpdateAuthorizationsRequest request) {
        return facade.updateKeyAuthorizations(keyId, request);
    }
}
