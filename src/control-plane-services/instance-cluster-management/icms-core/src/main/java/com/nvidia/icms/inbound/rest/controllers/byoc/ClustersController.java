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
package com.nvidia.icms.inbound.rest.controllers.byoc;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.byoc.ClusterGroupResponse;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.service.byoc.ClustersService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.annotation.Nullable;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "Registered clusters")
@RequestMapping(path = "/v1/si/accounts", produces = APPLICATION_JSON_VALUE)
@RequiredArgsConstructor
public class ClustersController {

    private final ClustersService clustersService;

    @GetMapping("{ncaId}/clusterGroups")
    @PreAuthorize("hasAuthority('cluster_listing')")
    @Operation(summary = "List of registered clusters and groups within the account",
            description = "Get the list of registered clusters and groups within the account",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "412", description = "Precondition Failed"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ClusterGroupResponse getRegisteredClusters(
            HttpServletRequest request,
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            String instanceTypeUsage) {

        return clustersService.getRegisteredClustersForNcaId(ncaId, validateInstanceTypeUsage(instanceTypeUsage));
    }

    private InstanceTypeUsageEnum validateInstanceTypeUsage(@Nullable String stringValue) {

        try {
            // If stringValue is not provided then returning default value
            if (StringUtils.isEmpty(stringValue)) {
                return InstanceTypeUsageEnum.DEFAULT;
            }

            // Validating and returning InstanceTypeUsageEnum value of provided stringValue
            return InstanceTypeUsageEnum.valueOf(stringValue);

        } catch (Exception exception) {
            log.error("Failed to convert {} string to InstanceTypeUsageEnum, error - {}",
                      stringValue, exception.getMessage(), exception);

            String errMsg = String.format(
                    "Invalid %s value for instanceTypeUsage provided, expected values: DEFAULT | CONTAINER",
                    stringValue);
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }
}
