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
package com.nvidia.nvcf.rest.client;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.client.dto.ClientDetailsResponse;
import com.nvidia.nvcf.service.client.ClientService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/clients", produces = APPLICATION_JSON_VALUE)
@Tag(name = "Client Management For NVIDIA Super Admins",
        description = """
                Defines Client Management endpoints. These endpoints can only be invoked by
                 NVIDIA Super Admins and require a bearer token in HTTP Authorization header with
                 'account_setup' scope.
                """
)
public class ClientController {

    private static final String CLIENT_ID_DESCRIPTION = "Client Id -- such as OAuth2 Client Id";
    private static final String AUTH_DESCRIPTION = """
            Requires a bearer token in the HTTP Authorization header with 'account_setup' scope.
             These endpoints are invoked by NVIDIA Super Admins working across accounts.
            """;

    private final ClientService clientService;

    @GetMapping(value = "{clientId}")
    @Operation(
            summary = "Get Client details",
            description = "Gets details of the specified client." + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAnyAuthority('account_setup', 'apikey:account_setup')")
    public ClientDetailsResponse getClientDetails(
            @Parameter(description = CLIENT_ID_DESCRIPTION, required = true)
            @PathVariable String clientId) {
        return new ClientDetailsResponse(clientService.getClient(clientId));
    }

}
