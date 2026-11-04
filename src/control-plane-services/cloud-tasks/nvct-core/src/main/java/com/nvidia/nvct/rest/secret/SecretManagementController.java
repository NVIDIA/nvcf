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
package com.nvidia.nvct.rest.secret;

import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvct.rest.secret.dto.UpdateSecretsRequest;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.ratelimit.RateLimiterService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v1/nvct/secrets", produces = APPLICATION_JSON_VALUE)
@Tag(name = "User Secret Management",
        description = """
                Defines User Secret Management endpoints for Account Admins. All the endpoints
                 defined in this API require a bearer token with appropriate scope in the
                 HTTP Authorization header.
                 """
)
public class SecretManagementController {

    private final SecretManagementFacade secretManagementFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @PutMapping(value = "tasks/{taskId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update user secrets for a task",
            description = """
                    Updates secrets for the specified task id. This endpoint requires a
                     bearer token 'update_secrets' scope in the HTTP Authorization header.
                    """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('update_secrets', 'apikey:update_secrets')")
    public ResponseEntity<Void> updateSecrets(
            @Parameter(description = "Task id", required = true)
            @NotNull @PathVariable UUID taskId,
            @Valid @NonNull @RequestBody UpdateSecretsRequest request,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, taskId);
        return secretManagementFacade.updateSecrets(ncaId, taskId, request.secrets(), authentication);
    }
}
