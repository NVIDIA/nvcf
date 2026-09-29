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
package com.nvidia.nvcf.rest.function.invocation;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenRequest;
import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenResponse;
import com.nvidia.nvcf.rest.function.invocation.dto.MultiFunctionsInvocationTokenRequest;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@Tag(name = "Function Invocation Assertion Token",
        description = """
                Defines endpoints that issue an assertion token from Notary Service
                to be used for function invocation later
                """
)
public class InvocationAssertionTokenController {

    public static final String DESC_200 = "Invocation assertion token is issued";
    public static final String DESC_403 = """
            Either missing scope in the auth(JWT / ApiKey) token and/or missing resource entry
             in the ApiKey for the function.
            """;
    public static final String DESC_429 =
            "Client is doing too many requests per second and should slow down request rate.";

    private final FunctionInvocationFacade functionInvocationFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;

    @PostMapping(value = {"/v2/nvcf/tokens/functions/{functionId}/versions/{functionVersionId}",
            "/v2/nvcf/tokens/functions/{functionId}"},
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Issue Function Invocation Token",
            description = """
                    Issues an assertion token from Notary Service for a specific functionId and/or functionVersionId
                    """,
            responses = {
                    @ApiResponse(responseCode = "200", description = DESC_200),
                    @ApiResponse(responseCode = "403", description = DESC_403),
                    @ApiResponse(responseCode = "429", description = DESC_429)
            }
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public InvocationTokenResponse issueInvocationToken(
            @Valid @RequestBody InvocationTokenRequest request,
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id",
                    schema = @Schema(types = {"string"}, format = "uuid"))
            @PathVariable @Nullable UUID functionVersionId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId, functionId, functionVersionId);
        return functionInvocationFacade.issueFunctionInvocationToken(request,
                                                                     ncaId,
                                                                     functionId,
                                                                     functionVersionId,
                                                                     authentication);

    }

    @PostMapping(value = "/v2/nvcf/tokens/functions", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Issue Function Invocation Token",
            description = """
                    Issues an assertion token from Notary Service for specified functionIds and/or functionVersionIds
                    """,
            responses = {
                    @ApiResponse(responseCode = "200", description = DESC_200),
                    @ApiResponse(responseCode = "403", description = DESC_403),
                    @ApiResponse(responseCode = "429", description = DESC_429)
            }
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public InvocationTokenResponse issueMultiFunctionsInvocationToken(
            @Valid @RequestBody MultiFunctionsInvocationTokenRequest request,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        rateLimiterService.verifyLimits(ncaId);
        return functionInvocationFacade.issueMultiFunctionsInvocationToken(request,
                                                                           ncaId,
                                                                           authentication);
    }

}
