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
package com.nvidia.nvcf.rest.queue;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.queue.dto.GetPositionInQueueResponse;
import com.nvidia.nvcf.rest.queue.dto.GetQueuesResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.annotation.Nullable;
import jakarta.validation.constraints.NotBlank;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Cross-Account Queue Details For NVIDIA Super Admins",
        description = """
                Defines Queue Details endpoints for NVIDIA Super Admins to work across
                 accounts. All the endpoints defined in this API require a bearer token
                 with 'admin:queue_details' scope in the HTTP Authorization header."""
)
public class XAccountQueueController {
    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";
    private static final String AUTH_DESCRIPTION = """
            Requires a bearer token with 'admin:queue_details' scope in the HTTP Authorization
             header.
            """;

    private final AccountService accountService;
    private final QueueFacade queueFacade;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @GetMapping({"/v2/nvcf/accounts/{ncaId}/queues/functions/{functionId}",
            "/v2/nvcf/accounts/{ncaId}/queues/functions/{functionId}/versions/{versionId}"})
    @Operation(
            summary = "Queue Details",
            description = """
                    Provides details of all the queues associated with the specified function.
                     If a function has multiple versions and they are all deployed, then the
                     response includes details of all the queues.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:queue_details')")
    public GetQueuesResponse getQueuesDetails(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @NotBlank @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id",
                    schema = @Schema(types = {"string"}, format = "uuid"))
            @PathVariable @Nullable UUID versionId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        return queueFacade.getQueuesDetails(ncaId, authentication, functionId, versionId);
    }

    @GetMapping(value = "/v2/nvcf/accounts/{ncaId}/queues/{requestId}/position")
    @Operation(
            summary = "Queue Position",
            description = """
                    Using the specified function invocation request id, returns the estimated
                     position of the corresponding message up to 1000 in the queue.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:queue_details')")
    public GetPositionInQueueResponse getPositionInQueue(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @NotBlank @PathVariable("ncaId") String ncaId,
            @Parameter(description = "Function invocation request id", required = true)
            @PathVariable("requestId") UUID requestId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        return queueFacade.getPositionInQueue(ncaId, requestId, authentication);
    }
}
