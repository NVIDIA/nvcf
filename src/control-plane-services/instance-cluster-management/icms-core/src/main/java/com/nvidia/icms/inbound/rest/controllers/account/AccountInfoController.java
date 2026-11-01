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
package com.nvidia.icms.inbound.rest.controllers.account;

import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.inbound.rest.model.InstanceTypeDetails;
import com.nvidia.icms.inbound.rest.model.account.DeploymentGpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.GpuUsageResponse;
import com.nvidia.icms.inbound.rest.model.account.InstanceTypeAvailabilityResponse;
import com.nvidia.icms.inbound.rest.model.byoc.InstanceTypeUsageEnum;
import com.nvidia.icms.service.account.AccountInfoService;
import com.nvidia.icms.service.account.GpuUsageFilter;
import com.nvidia.icms.service.account.GpuUsageService;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Map;
import java.util.Set;
import jakarta.annotation.Nullable;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

/**
 * GET methods related to registered clusters in an account.
 */
@Slf4j
@RestController
@Tag(name = "Account Info APIs")
@RequestMapping(path = "/v1/si/accounts", produces = APPLICATION_JSON_VALUE)
@RequiredArgsConstructor
public class AccountInfoController {

    private final AccountInfoService accountInfoService;
    private final GpuUsageService gpuUsageService;

    @GetMapping("{ncaId}/gpus")
    @PreAuthorize("hasAuthority('gpu_listing')")
    @Operation(summary = "List of registered GPUs authorized for the account",
            description = "Get the list of registered GPUs authorized for the account",
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
    public ResponseEntity<Map<String, Set<String>>> getGpus(
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            Set<String> backends,
            @RequestParam(required = false)
            String instanceTypeUsage,
            @RequestParam(required = false, defaultValue = "true")
            boolean capacityValidation
    ) {
        return ResponseEntity.status(
                HttpStatus.OK).body(accountInfoService.getAllGpusForAccount(
                        ncaId,
                        GpuUsageFilter.builder()
                                .clusterGroupNames(backends)
                                .instanceTypeUsageFilter(toInstanceTypeUsage(instanceTypeUsage))
                                .validateCapacity(capacityValidation)
                                .build()));
    }

    @GetMapping("{ncaId}/regions")
    @PreAuthorize("hasAuthority('regions_listing')")
    @Operation(summary = "List of available regions for the account",
            description = "Get the list of available regions for the account",
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
    public ResponseEntity<Map<String, Set<String>>> getRegions(
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            Set<String> clusters,
            @RequestParam(required = false)
            Set<String> attributes,
            @RequestParam(required = false)
            Set<String> gpus,
            @RequestParam(required = false)
            Set<String> backends,
            @RequestParam(required = false)
            String instanceTypeUsage,
            @RequestParam(required = false, defaultValue = "true")
            boolean capacityValidation
    ) {
        return ResponseEntity
                .status(HttpStatus.OK)
                .body(accountInfoService.getAvailableRegions(
                        ncaId,
                        GpuUsageFilter.builder()
                                .gpuNames(gpus)
                                .clusterGroupNames(backends)
                                .clusterNames(clusters)
                                .attributes(attributes)
                                .instanceTypeUsageFilter(toInstanceTypeUsage(instanceTypeUsage))
                                .validateCapacity(capacityValidation)
                                .build()));
    }

    @GetMapping("{ncaId}/clusterNames")
    @PreAuthorize("hasAuthority('clusters_listing')")
    @Operation(summary = "List of available clusters for the account",
            description = "Get the list of available clusters for the account",
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
    public ResponseEntity<Map<String, Set<String>>> getClusterNames(
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            Set<String> regions,
            @RequestParam(required = false)
            Set<String> attributes,
            @RequestParam(required = false)
            Set<String> gpus,
            @RequestParam(required = false)
            Set<String> backends,
            @RequestParam(required = false)
            String instanceTypeUsage,
            @RequestParam(required = false, defaultValue = "true")
            boolean capacityValidation
    ) {
        return ResponseEntity
                .status(HttpStatus.OK)
                .body(accountInfoService.getAvailableClusterNames(
                        ncaId,
                        GpuUsageFilter.builder()
                                .gpuNames(gpus)
                                .clusterGroupNames(backends)
                                .regionNames(regions)
                                .attributes(attributes)
                                .instanceTypeUsageFilter(toInstanceTypeUsage(instanceTypeUsage))
                                .validateCapacity(capacityValidation)
                                .build()));
    }

    @GetMapping("{ncaId}/attributes")
    @PreAuthorize("hasAuthority('attributes_listing')")
    @Operation(summary = "List of available attributes within all the clusters for the account",
            description = "Get the list of available attributes within all the clusters for the account",
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
    public ResponseEntity<Map<String, Set<String>>> getAttributes(
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            Set<String> regions,
            @RequestParam(required = false)
            Set<String> clusters,
            @RequestParam(required = false)
            Set<String> gpus,
            @RequestParam(required = false)
            Set<String> backends,
            @RequestParam(required = false)
            String instanceTypeUsage,
            @RequestParam(required = false, defaultValue = "true")
            boolean capacityValidation
    ) {
        return ResponseEntity
                .status(HttpStatus.OK)
                .body(accountInfoService.getAvailableAttributes(
                        ncaId,
                        GpuUsageFilter.builder()
                                .gpuNames(gpus)
                                .clusterGroupNames(backends)
                                .regionNames(regions)
                                .clusterNames(clusters)
                                .instanceTypeUsageFilter(toInstanceTypeUsage(instanceTypeUsage))
                                .validateCapacity(capacityValidation)
                                .build()));
    }

    @GetMapping("{ncaId}/instanceTypes")
    @PreAuthorize("hasAuthority('instance_types')")
    @Operation(summary = """
            List of available instance types with clusters, regions, attributes for each
            instance type within all the clusters for the account""",
            description = """
                    Get the list of available instance types with clusters, regions, attributes for
                    each instance type within all the clusters for the account""",
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
    public ResponseEntity<Map<String, Set<InstanceTypeDetails>>> getInstanceTypes(
            @Schema(description = "Nvidia Cloud Account Id")
            @PathVariable("ncaId") String ncaId,
            @RequestParam(required = false)
            Set<String> regions,
            @RequestParam(required = false)
            Set<String> clusters,
            @RequestParam(required = false)
            Set<String> attributes,
            @RequestParam(required = false)
            Set<String> gpus,
            @RequestParam(required = false)
            Set<String> backends,
            @RequestParam(required = false)
            String instanceType,
            @RequestParam(required = false)
            String instanceTypeUsage,
            @RequestParam(required = false, defaultValue = "true")
            boolean capacityValidation
    ) {
        GpuUsageFilter filter = GpuUsageFilter.builder()
                .gpuNames(gpus)
                .clusterGroupNames(backends)
                .instanceTypes(instanceType != null && !StringUtils.isBlank(instanceType)? Set.of(instanceType) : null)
                .regionNames(regions)
                .clusterNames(clusters)
                .attributes(attributes)
                .instanceTypeUsageFilter(toInstanceTypeUsage(instanceTypeUsage))
                .validateCapacity(capacityValidation)
                .build();

        return ResponseEntity.status(HttpStatus.OK)
                .body(accountInfoService.getAvailableInstanceTypes(ncaId, filter));
    }

    @GetMapping("{ncaId}/instanceTypes/availability")
    @PreAuthorize("hasAuthority('spot-gpu-usage') or hasAuthority('gpu-usage')")
    @Operation(summary = "Get instance type availability information",
            description = "Retrieves information about available instance types and their GPU configurations across different regions and clusters",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(schema = @Schema(implementation = InstanceTypeAvailabilityResponse.class))
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "NCA not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<InstanceTypeAvailabilityResponse> getInstanceTypeAvailability(
            HttpServletRequest request,
            @Schema(description = "NCA ID for which to retrieve instance type availability information")
            @PathVariable("ncaId") String ncaId) {
        log.debug("Retrieving instance type availability information for NCA: {}", ncaId);
        return ResponseEntity.ok(accountInfoService.getInstanceTypeAvailability(ncaId));
    }


    @GetMapping("{ncaId}/gpu/usage")
    @PreAuthorize("hasAuthority('spot-gpu-usage') or hasAuthority('gpu-usage')")
    @Operation(summary = "Get GPU usage information",
            description = "Retrieves information about GPU usage across different regions and clusters for a specific NCA",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(schema = @Schema(implementation = GpuUsageResponse.class))
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "NCA not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<GpuUsageResponse> getGpuUsage(
            HttpServletRequest request,
            @Schema(description = "NCA ID for which to retrieve GPU usage information")
            @PathVariable("ncaId") String ncaId) {
        log.debug("Retrieving GPU usage information for NCA: {}", ncaId);
        return ResponseEntity.ok(gpuUsageService.getGpuUsage(ncaId));
    }

    @GetMapping("{ncaId}/deployments/{deploymentId}/gpu/usage")
    @PreAuthorize("hasAuthority('spot-gpu-usage') or hasAuthority('gpu-usage')")
    @Operation(summary = "Get GPU usage information for a specific deployment",
            description = "Retrieves information about GPU usage across different regions and clusters for a specific deployment within an NCA",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(schema = @Schema(implementation = DeploymentGpuUsageResponse.class))
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "NCA or deployment not found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<DeploymentGpuUsageResponse> getDeploymentGpuUsage(
            HttpServletRequest request,
            @Schema(description = "NCA ID for which to retrieve GPU usage information")
            @PathVariable("ncaId") String ncaId,
            @Schema(description = "Deployment ID for which to retrieve GPU usage information")
            @PathVariable("deploymentId") String deploymentId) {
        log.debug("Retrieving GPU usage information for NCA: {} and deployment: {}", ncaId,
                  deploymentId);
        return ResponseEntity.ok(gpuUsageService.getDeploymentGpuUsage(ncaId, deploymentId));
    }




    private InstanceTypeUsageEnum toInstanceTypeUsage(@Nullable String stringValue) {

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
