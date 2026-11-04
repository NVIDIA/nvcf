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
package com.nvidia.nvct.rest.event;

import static com.nvidia.nvct.util.NvctConstants.DEFAULT_PAGINATION_LIMIT;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvct.rest.event.dto.ListEventsResponse;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.constraints.Positive;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Cross-Account Event Support for NVIDIA Super Admins",
        description = """
                Defines Event endpoints for NVIDIA Super Admins to work across accounts. All the
                 endpoints defined in this API require a bearer token with an appropriate
                 admin scope in the HTTP Authorization header.
                 """
)
public class XAccountEventController {

    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account(NCA) Id";

    private final AccountService accountService;
    private final EventFacade eventFacade;
    private final Tracer tracer;

    @GetMapping("/v1/nvct/accounts/{ncaId}/tasks/{taskId}/events")
    @Operation(
            summary = "Get Events",
            description = """
                    Gets events associated with the specified task in the authenticated
                     NVIDIA Cloud Account. Requires a bearer token with 'admin:list_events'
                     scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAuthority('admin:list_events')")
    public ListEventsResponse getTaskEvents(
            @Parameter(description = "Number of events to return in the response")
            @RequestParam(required = false, defaultValue = DEFAULT_PAGINATION_LIMIT)
            @Positive int limit,
            @Parameter(description = "Beginning of the pagination slice")
            @RequestParam(required = false) String cursor,
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Task id", required = true)
            @PathVariable("taskId") UUID taskId,
            Authentication authentication) {
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return eventFacade.getEvents(ncaId, taskId, limit, cursor, authentication);
    }

}
