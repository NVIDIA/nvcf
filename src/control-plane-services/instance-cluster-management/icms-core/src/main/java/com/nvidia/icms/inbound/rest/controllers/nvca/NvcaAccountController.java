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
package com.nvidia.icms.inbound.rest.controllers.nvca;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationRequest;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterCreationResponse;
import com.nvidia.icms.inbound.rest.model.nvca.ClusterUpdateRequest;
import com.nvidia.icms.inbound.rest.model.nvca.GetClusterResponse;
import com.nvidia.icms.service.ClusterManagementService;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import java.util.List;
import java.util.Map;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "NVIDIA Cluster Agent Account Controller")
@RequestMapping(path = {"/v1/accounts/", "/v1/si/accounts/"}, produces = APPLICATION_JSON_VALUE)
public class NvcaAccountController {

    @Autowired
    private ClusterManagementService clusterManagementService;

    @PostMapping("{ncaId}/clusters")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "NVCA Cluster Creation",
            description = "API to create a new NVCA cluster",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = ClusterCreationRequest.class)
                    )
            ))
    public ClusterCreationResponse createCluster(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId,
            @Valid @RequestBody ClusterCreationRequest clusterCreationRequest) {
        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        return clusterManagementService.clusterCreation(clusterCreationRequest, ncaId, auditProps);
    }

    @GetMapping("{ncaId}/clusters/{clusterId}")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "Get NVCA Cluster",
            description = "API to get the cluster details for ncaId and clusterId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Cluster not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public GetClusterResponse getClusterForNcaIdAndClusterId(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId,
            @Schema(description = "Cluster ID")
            @PathVariable("clusterId")
            String clusterId) {
        return clusterManagementService.getCluster(ncaId, clusterId);
    }

    @GetMapping("{ncaId}/clusterVersions")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "Get NVCA version details",
            description = "API to get the NVCA version details",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public String getClusterVersion(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId) {
        return clusterManagementService.getClusterVersion(ncaId);
    }

    @GetMapping("{ncaId}/clusters")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "Get NVCA Clusters",
            description = "API to get the cluster details for given NcaId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Cluster not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public List<GetClusterResponse> getClusters(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId,

            @Schema(description = "Set this param to include all clusters authorized to given ncaId")
            @RequestParam(required = false)
            Boolean includeAuthorizedClusters,

            @Schema(description = "If includeAuthorizedClusters=true then this param will be considered else ignored")
            @RequestParam(name = "includeNonByocInAuthorizedClusters", required = false)
            Boolean includeNonByocInAuthorizedClusters,

            @Schema(description = "Deprecated alias for includeNonByocInAuthorizedClusters",
                    deprecated = true)
            @RequestParam(name = "includeGfnInAuthorizedClusters", required = false)
            Boolean includeGfnInAuthorizedClusters) {
        // Honour the legacy param name only when the current one is absent, and keep passing null
        // through untouched when neither is supplied so behaviour is unchanged for existing callers.
        Boolean includeNonByoc = includeNonByocInAuthorizedClusters;
        if (includeNonByoc == null && includeGfnInAuthorizedClusters != null) {
            includeNonByoc = includeGfnInAuthorizedClusters;
        }
        return clusterManagementService.getClusters(ncaId, includeAuthorizedClusters, includeNonByoc);
    }

    @DeleteMapping("{ncaId}/clusters/{clusterId}")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "Delete NVCA Cluster",
            description = "API to delete the cluster for ncaId and clusterId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Cluster not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public void deleteClusterForNcaIdAndClusterId(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId,
            @Schema(description = "Cluster ID")
            @PathVariable("clusterId")
            String clusterId) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        clusterManagementService.deleteCluster(ncaId, clusterId, auditProps);
    }

    @PutMapping("{ncaId}/clusters/{clusterId}")
    @PreAuthorize("hasAuthority('cluster-management')")
    @Operation(summary = "Update NVCA Cluster",
            description = "API to update the cluster for ncaId and clusterId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Cluster not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "412", description = "Conflict"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            },
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = ClusterUpdateRequest.class)
                    )
            ))
    public void updateClusterForNcaIdAndClusterId(
            HttpServletRequest request,
            @Schema(description = "NVIDIA Cloud Account ID")
            @PathVariable("ncaId")
            String ncaId,
            @Schema(description = "Cluster ID")
            @PathVariable("clusterId")
            String clusterId,
            @Valid @RequestBody ClusterUpdateRequest clusterUpdateRequest) {

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);
        clusterManagementService.updateCluster(clusterUpdateRequest, ncaId, clusterId, auditProps);
    }

}
