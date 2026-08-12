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
package com.nvidia.icms.inbound.rest.controllers;

import static com.nvidia.icms.inbound.rest.converters.ErrorDataConverter.toUnifiedErrorData;
import static com.nvidia.icms.uec.IcmsUnifiedError.NVCF_INCORRECT_PARAMETER;
import static com.nvidia.icms.uec.UnifiedErrorData.stringFromUuid;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsConflictException;
import com.nvidia.icms.inbound.rest.converters.CreateInstanceApiModelConverter;
import com.nvidia.icms.inbound.rest.converters.InstanceModelConverter;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstanceRequestApiModel;
import com.nvidia.icms.inbound.rest.model.CreateSpotInstancesResponse;
import com.nvidia.icms.inbound.rest.model.GetActiveInstanceInfoResponse;
import com.nvidia.icms.inbound.rest.model.GetSpotInstanceRequests;
import com.nvidia.icms.inbound.rest.model.OverrideBillingRequest;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestAction;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.inbound.rest.model.TerminateInstancesResponse;
import com.nvidia.icms.inbound.rest.model.instance.GetInstanceRequestsResponse;
import com.nvidia.icms.inbound.rest.model.instance.TerminateInstanceRequestsResponse;
import com.nvidia.icms.inbound.rest.model.swagger.schema.SpotInstanceRequestSchema;
import com.nvidia.icms.service.InstanceService;
import com.nvidia.icms.uec.IcmsHttpUnifiedErrorException;
import com.nvidia.icms.uec.UnifiedErrorException;
import com.nvidia.icms.uec.UnifiedErrorData;
import com.nvidia.icms.uec.UnifiedErrorReporter;
import com.nvidia.icms.util.AuthUtils;
import com.nvidia.icms.util.audit.AuditUtils;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.media.Content;
import io.swagger.v3.oas.annotations.media.Schema;
import io.swagger.v3.oas.annotations.parameters.RequestBody;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.annotation.Nullable;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import java.time.Instant;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.stream.Collectors;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springdoc.core.annotations.ParameterObject;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@Tag(name = "Instance")
@RequestMapping(path = "/v1/si", produces = APPLICATION_JSON_VALUE)
@AllArgsConstructor
public class InstanceController {

    private final InstanceService instanceService;

    private final CreateInstanceApiModelConverter converter;

    private final UnifiedErrorReporter unifiedErrorReporter;

    @PostMapping
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Request new instances",
            description = "An asynchronous request to create new instances",
            responses = {
                    @ApiResponse(
                            responseCode = "202",
                            description = "Accepted"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            },
            requestBody = @RequestBody(
                    content = @Content(
                            mediaType = MediaType.APPLICATION_FORM_URLENCODED_VALUE,
                            schema = @Schema(implementation = SpotInstanceRequestSchema.class)
                    )
            ))
    public ResponseEntity<CreateSpotInstancesResponse> requestInstances(
            HttpServletRequest request,
            @ParameterObject CreateSpotInstanceRequestApiModel createRequest) {
        SpotInstanceRequestSchema instanceRequestSchema = converter.toSpotInstanceRequestSchema(
                createRequest);

        validateInstancesRequest(instanceRequestSchema);

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        CreateSpotInstancesResponse createInstancesResponse =
                instanceService.requestInstances(AuthUtils.getSubFromSecurityContext(),
                                                 instanceRequestSchema,
                                                 auditProps);
        return ResponseEntity.status(HttpStatus.ACCEPTED).body(createInstancesResponse);
    }

    @GetMapping
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Describe instance requests or describe instances",
            description = "Request to describe instance requests or describe instances based on the action specified",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(
                                    mediaType = APPLICATION_JSON_VALUE,
                                    schema = @Schema(oneOf = {
                                            GetSpotInstanceRequests.class,
                                            GetInstanceRequestsResponse.class
                                    })
                            )
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<?> describeInstances(
            HttpServletRequest request,
            @Schema(allowableValues = {"DescribeSpotInstanceRequests",
                    "DescribeInstanceRequests",
                    "DescribeInstances"}, description = ", " +
                    "Use a describe instance requests Action and provide a comma separated list of requestId" +
                    " in the legacy request-id parameter or its InstanceRequestId alias for describing instance requests" +
                    ", Other params can be optional. " +

                    "Use DescribeInstances as Action and provide a comma separated list of instanceId" +
                    " in InstanceId param for describing instances" +
                    ", Other params can be optional. "
            )
            @RequestParam(name = "Action") String action,

            @Schema(description = "comma separated list of request IDs for description when Action describes instance requests. Interchangeable with InstanceRequestId; both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "SpotInstanceRequestId", required = false)
            Set<String> legacyInstanceRequestIds,

            @Schema(description = "Alias for the legacy request-id parameter. Both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "InstanceRequestId", required = false)
            Set<String> instanceRequestIds,

            @Schema(description = "comma separated list of instance IDs for description when Action is DescribeInstances.")
            @RequestParam(name = "InstanceId", required = false)
            List<String> instanceIds,

            @Schema(description =
                    "comma separated list of request state. This will be considered with Action=DescribeSpotInstanceRequests or DescribeInstanceRequests.",
                    allowableValues = {"open", "closed", "active", "canceled"})
            @RequestParam(name = "SpotStateFilter", required = false)
            Set<String> stateFilter) {
        Optional<SpotInstanceRequestAction> requestAction = SpotInstanceRequestAction.toSpotInstanceRequestAction(
                action);

        Set<String> requestIds = mergeRequestIds(legacyInstanceRequestIds, instanceRequestIds);

        if (requestAction.isEmpty()) {
            throwExceptionInvalidAction(action, "Invalid", null);
        } else if (requestAction.get()
                == SpotInstanceRequestAction.DESCRIBE_SPOT_INSTANCE_REQUESTS) {
            return ResponseEntity.ok(
                    instanceService.describeInstanceRequests(AuthUtils.getSubFromSecurityContext(),
                                                     requestIds,
                                                     validateState(requestIds,
                                                                   stateFilter)));
        } else if (requestAction.get()
                == SpotInstanceRequestAction.DESCRIBE_INSTANCE_REQUESTS) {
            return ResponseEntity.ok(InstanceModelConverter.toGetInstanceRequestsResponse(
                    instanceService.describeInstanceRequests(AuthUtils.getSubFromSecurityContext(),
                                                     requestIds,
                                                     validateState(requestIds,
                                                                   stateFilter))));
        } else if (requestAction.get() == SpotInstanceRequestAction.DESCRIBE_INSTANCES) {
            return ResponseEntity.ok(
                    instanceService.describeInstances(AuthUtils.getSubFromSecurityContext(),
                                                      instanceIds));
        } else {
            throwExceptionInvalidAction(action, "Invalid", null);
        }

        return ResponseEntity.noContent().build();
    }

    @GetMapping({"/accounts/{ncaId}/deployments/{workloadId}/instances",
            "/accounts/{ncaId}/tasks/{workloadId}/instances",
            "/accounts/{ncaId}/workloads/{workloadId}/instances"})
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Describe instances based on ncaId and deploymentId",
            description = "Request to describe instances based on ncaId and deploymentId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(
                                    mediaType = APPLICATION_JSON_VALUE,
                                    schema = @Schema(oneOf = {
                                            GetSpotInstanceRequests.class,
                                            GetInstanceRequestsResponse.class
                                    })
                            )
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<?> describeInstancesPerDeployment(
            HttpServletRequest request,
            @Schema(description = "nca id of function owner")
            @NotNull @PathVariable("ncaId")
            String ncaId,

            @Schema(description = "NVCF deployment id or NVCT task id")
            @NotNull @PathVariable("workloadId")
            UUID workloadId,

            @Schema(description = "Filters real lifecycle-terminated instances (DB rows with state=terminated). "
                    + "When false (default), real terminated instances are excluded from the response - typically "
                    + "used by long-running deployments to keep the payload small. "
                    + "Does NOT affect synthetic terminated placeholders emitted by ExpiredAckedInstances; "
                    + "those represent failed/expired ACKs, not lifecycle terminations.")
            @RequestParam(name = "IncludeTerminated", required = false, defaultValue = "false")
            Boolean includeTerminated,

            @Schema(description = "If true, return new GetInstanceRequestsResponse response model " +
                    "(e.g. InstanceRequests/Instances instead of SpotInstanceRequests/Instances).")
            @RequestParam(name = "UseConciseName", required = false, defaultValue = "false")
            Boolean useConciseName,

            @Schema(description = "Surfaces acknowledged-but-unmaterialized instances as synthetic terminated "
                    + "placeholders (with HealthInfo.ErrorLog) when the request has been canceled, the ACK batch "
                    + "has expired, or the cluster has gone unhealthy. "
                    + "When false (default), those failure cases are silently dropped from the response. "
                    + "When true, typically used by deployments that are still in their initial scale-up phase "
                    + "to see why expected instances did not come up. "
                    + "Independent of IncludeTerminated - that flag only governs real DB rows.")
            @RequestParam(name = "ExpiredAckedInstances", required = false, defaultValue = "false")
            Boolean expiredAckedInstances
            ) {

        if (StringUtils.isBlank(ncaId)) {
            throw new IcmsBadRequestException("ncaId should be provided");
        }

        return toDescribeInstancesResponse(
                instanceService.describeInstancesByDeploymentId(
                        ncaId, workloadId, null,
                        Boolean.TRUE.equals(includeTerminated),
                        expiredAckedInstances != null && expiredAckedInstances),
                useConciseName);
    }

    @GetMapping({"/accounts/{ncaId}/deployments/{workloadId}/gpuSpecs/{gpuSpecId}/instances",
            "/accounts/{ncaId}/tasks/{workloadId}/gpuSpecs/{gpuSpecId}/instances",
            "/accounts/{ncaId}/workloads/{workloadId}/gpuSpecs/{gpuSpecId}/instances"})
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Describe instances based on ncaId, deploymentId or taskId and gpuSpecId",
               description = "Request to describe instances based on ncaId, deploymentId or taskId and gpuSpecId",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(
                                    mediaType = APPLICATION_JSON_VALUE,
                                    schema = @Schema(oneOf = {
                                            GetSpotInstanceRequests.class,
                                            GetInstanceRequestsResponse.class
                                    })
                            )
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<?> describeInstancesPerGpuSpec(
            HttpServletRequest request,
            @Schema(description = "nca id of function owner")
            @NotNull @PathVariable("ncaId")
            String ncaId,

            @Schema(description = "NVCF deployment id or NVCT task id")
            @NotNull @PathVariable("workloadId")
            UUID workloadId,

            @Schema(description = "GPU Specification id")
            @NotNull @PathVariable("gpuSpecId")
            UUID gpuSpecId,

            @Schema(description = "Filters real lifecycle-terminated instances (DB rows with state=terminated). "
                    + "When false (default), real terminated instances are excluded from the response - typically "
                    + "used by long-running deployments to keep the payload small. "
                    + "Does NOT affect synthetic terminated placeholders emitted by ExpiredAckedInstances; "
                    + "those represent failed/expired ACKs, not lifecycle terminations.")
            @RequestParam(name = "IncludeTerminated", required = false, defaultValue = "false")
            Boolean includeTerminated,

            @Schema(description = "If true, return new GetInstanceRequestsResponse response model " +
                    "(e.g. InstanceRequests/Instances instead of SpotInstanceRequests/Instances).")
            @RequestParam(name = "UseConciseName", required = false, defaultValue = "false")
            Boolean useConciseName,

            @Schema(description = "Surfaces acknowledged-but-unmaterialized instances as synthetic terminated "
                    + "placeholders (with HealthInfo.ErrorLog) when the request has been canceled, the ACK batch "
                    + "has expired, or the cluster has gone unhealthy. "
                    + "When false (default), those failure cases are silently dropped from the response. "
                    + "When true, typically used by deployments that are still in their initial scale-up phase "
                    + "to see why expected instances did not come up. "
                    + "Independent of IncludeTerminated - that flag only governs real DB rows.")
            @RequestParam(name = "ExpiredAckedInstances", required = false, defaultValue = "false")
            Boolean expiredAckedInstances) {

        if (StringUtils.isBlank(ncaId)) {
            throw new IcmsBadRequestException("ncaId should be provided");
        }

        return toDescribeInstancesResponse(
                instanceService.describeInstancesByDeploymentId(
                        ncaId, workloadId, gpuSpecId,
                        Boolean.TRUE.equals(includeTerminated),
                        expiredAckedInstances != null && expiredAckedInstances),
                useConciseName);
    }


    @DeleteMapping
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Terminate instances",
            description = "Request to terminate instances",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(
                                    mediaType = APPLICATION_JSON_VALUE,
                                    schema = @Schema(oneOf = {
                                            TerminateInstancesResponse.class,
                                            TerminateInstanceRequestsResponse.class
                                    })
                            )
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<?> terminateInstances(
            HttpServletRequest request,
            @Schema(allowableValues = {
                    "TerminateInstances",
                    "TerminateSpotInstanceRequest",
                    "TerminateInstanceRequest"}, description = "Action for instance terminate request")
            @RequestParam(name = "Action") String action,

            @Schema(description = "comma separated list of instance IDs for termination")
            @RequestParam(name = "InstanceId", required = false)
            Set<String> instanceIds,

            @Schema(description = "a Request Id for termination")
            @RequestParam(name = "RequestId", required = false)
            String requestId) {

        Optional<SpotInstanceRequestAction> requestAction = SpotInstanceRequestAction.toSpotInstanceRequestAction(
                action);

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        if (requestAction.isEmpty()) {
            throwExceptionInvalidAction(action, "Invalid", null);
        } else if (requestAction.get() == SpotInstanceRequestAction.TERMINATE_INSTANCES) {
            if (instanceIds == null || instanceIds.isEmpty()) {
                throw new IcmsBadRequestException(
                        String.format("InstanceId should be provided with %s action", action));
            }
            return ResponseEntity.ok(
                    instanceService.terminateInstances(AuthUtils.getSubFromSecurityContext(),
                                                        instanceIds, auditProps));
        } else if (requestAction.get()
                == SpotInstanceRequestAction.TERMINATE_SPOT_INSTANCE_REQUEST) {
            if (StringUtils.isEmpty(requestId)) {
                throw new IcmsBadRequestException(
                        String.format("RequestId should be provided with %s action", action));
            }
            return ResponseEntity.ok(
                    instanceService.terminateInstanceRequests(AuthUtils.getSubFromSecurityContext(),
                                                       Set.of(requestId), auditProps));
        } else if (requestAction.get()
                == SpotInstanceRequestAction.TERMINATE_INSTANCE_REQUEST) {
            if (StringUtils.isEmpty(requestId)) {
                throw new IcmsBadRequestException(
                        String.format("RequestId should be provided with %s action", action));
            }
            return ResponseEntity.ok(InstanceModelConverter.toTerminateInstanceRequestsResponse(
                    instanceService.terminateInstanceRequests(AuthUtils.getSubFromSecurityContext(),
                                                       Set.of(requestId), auditProps)));
        } else {
            throwExceptionInvalidAction(action, "Invalid", null);
        }

        return ResponseEntity.noContent().build();
    }

    @DeleteMapping("/accounts/{ncaId}/instances/{instanceId}")
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Terminate instance",
            description = "Request to terminate instance",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public TerminateInstancesResponse deleteInstancePerDeployment(
            HttpServletRequest request,
            @Schema(description = "nca id of function owner")
            @PathVariable("ncaId")
            @NotNull String ncaId,

            @Schema(description = "Instance id")
            @PathVariable("instanceId")
            @NotNull String instanceId) {

        if (StringUtils.isBlank(ncaId)) {
            throw new IcmsBadRequestException("ncaId should be provided");
        }

        if (StringUtils.isBlank(instanceId)) {
            throw new IcmsBadRequestException("instanceId should be provided");
        }

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        return instanceService.terminateInstances(ncaId, null,null, instanceId, auditProps);
    }

    @DeleteMapping({"/accounts/{ncaId}/deployments/{workloadId}",
            "/accounts/{ncaId}/tasks/{workloadId}",
            "/accounts/{ncaId}/workloads/{workloadId}"})
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Terminate instance deployment",
            description = "Request to terminate instance deployment",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public TerminateInstancesResponse deleteInstanceDeployment(
            HttpServletRequest request,
            @Schema(description = "nca id of function owner")
            @PathVariable("ncaId")
            @NotNull String ncaId,

            @Schema(description = "NVCF deployment id or NVCT task id")
            @PathVariable("workloadId")
            @NotNull UUID workloadId) {

        if (StringUtils.isBlank(ncaId)) {
            throw new IcmsBadRequestException("ncaId should be provided");
        }

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        return instanceService.instanceDeploymentTermination(ncaId, workloadId,null, auditProps);
    }


    @DeleteMapping({"/accounts/{ncaId}/deployments/{workloadId}/gpuSpecs/{gpuSpecId}",
            "/accounts/{ncaId}/tasks/{workloadId}/gpuSpecs/{gpuSpecId}",
            "/accounts/{ncaId}/workloads/{workloadId}/gpuSpecs/{gpuSpecId}"})
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Terminate instance deployment",
            description = "Request to terminate instance deployment",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public TerminateInstancesResponse deleteInstanceDeploymentByGpuSpec(
            HttpServletRequest request,
            @Schema(description = "nca id of function owner")
            @PathVariable("ncaId")
            @NotNull String ncaId,

            @Schema(description = "NVCF deployment id or NVCT task id")
            @PathVariable("workloadId")
            @NotNull UUID workloadId,

            @Schema(description = "NVCF GPU Specification id")
            @PathVariable("gpuSpecId")
            @NotNull UUID gpuSpecId) {


        if (StringUtils.isBlank(ncaId)) {
            throw new IcmsBadRequestException("ncaId should be provided");
        }

        Map<String, Object> auditProps = AuditUtils.getAuditPropertiesFromRequest(request);

        return instanceService.instanceDeploymentTermination(ncaId, workloadId,gpuSpecId, auditProps);
    }

    @PutMapping
    @PreAuthorize("hasAuthority('spot-request') or hasAuthority('instance-request')")
    @Operation(summary = "Cancel instance requests",
            description = "Request to cancel open instance requests",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "409", description = "Conflict"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public void cancelInstanceRequests(
            HttpServletRequest request,
            @Schema(allowableValues = {"CancelSpotInstanceRequests", "CancelInstanceRequests"},
                    description = "Action for canceling open instance request")
            @RequestParam(name = "Action")
            String action,

            @Schema(description = "comma separated list of open request IDs for cancellation. Interchangeable with InstanceRequestId; both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "SpotInstanceRequestId", required = false)
            Set<String> legacyInstanceRequestIds,

            @Schema(description = "Alias for the legacy request-id parameter. Both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "InstanceRequestId", required = false)
            Set<String> instanceRequestIds) {

        Optional<SpotInstanceRequestAction> requestAction = SpotInstanceRequestAction.toSpotInstanceRequestAction(
                action);

        Set<String> requestIds = mergeRequestIds(legacyInstanceRequestIds, instanceRequestIds);
        if (requestIds == null || requestIds.isEmpty()) {
            throw new IcmsBadRequestException(
                    "Either SpotInstanceRequestId or InstanceRequestId must be provided");
        }

        if (requestAction.isEmpty()
                || (requestAction.get() != SpotInstanceRequestAction.CANCEL_SPOT_INSTANCE_REQUESTS
                && requestAction.get() != SpotInstanceRequestAction.CANCEL_INSTANCE_REQUESTS)) {
            throwExceptionInvalidAction(action, "Invalid", null);
        } else {
            instanceService.cancelInstanceRequests(AuthUtils.getSubFromSecurityContext(),
                                                   requestIds,
                                                   AuditUtils.getAuditPropertiesFromRequest(
                                                           request));
        }
    }

    @PostMapping("billing")
    @PreAuthorize("hasAuthority('billing_override') or hasAuthority('apikey:billing_override')")
    @Operation(summary = "Override the billing nca id of a NVCF function and version",
            description = "Request to override the billing nca id of all instances associated to specified NVCF function and version",
            requestBody = @io.swagger.v3.oas.annotations.parameters.RequestBody(
                    content = @Content(
                            mediaType = APPLICATION_JSON_VALUE,
                            schema = @Schema(implementation = OverrideBillingRequest.class)
                    )
            ),
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
    public void overrideBilling(
            @Valid @org.springframework.web.bind.annotation.RequestBody OverrideBillingRequest request) {
        instanceService.overrideBilling(request);
    }

    @GetMapping("/clusters/{zoneName}/instances")
    @PreAuthorize("hasAuthority('cluster-instances') or hasAuthority('nvca-cluster') "
            + "or hasAuthority('apikey:nvca-cluster')")
    @Operation(summary = "Get instances from zone or cluster",
            description = "Request to get instances from zone or cluster",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK"
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "412", description = "Precondition failed"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public GetActiveInstanceInfoResponse getActiveInstancesForZone(
            HttpServletRequest request,
            @Schema(description = "zoneName or clusterId")
            @PathVariable("zoneName")
            String zoneName) {
        return instanceService.getActiveInstancesForZone(zoneName);
    }

    // ADMIN APIs
    @GetMapping("/admin")
    @PreAuthorize("hasAuthority('admin:spot-request:describe') or hasAuthority('admin:instance-request:describe')")
    @Operation(summary = "Describe any instance requests",
            description = "Request to describe instance requests based on the action specified",
            responses = {
                    @ApiResponse(
                            responseCode = "200",
                            description = "OK",
                            content = @Content(
                                    mediaType = APPLICATION_JSON_VALUE,
                                    schema = @Schema(oneOf = {
                                            GetSpotInstanceRequests.class,
                                            GetInstanceRequestsResponse.class
                                    })
                            )
                    ),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "400", description = "Bad request"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "401", description = "Unauthorized"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "403", description = "Forbidden - invalid token provided"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "404", description = "Not Found"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "429", description = "Too many requests"),
                    @ApiResponse(content = @Content(schema = @Schema(hidden = true)), responseCode = "500", description = "Internal server error")
            })
    public ResponseEntity<?> describeAdminInstanceRequests(
            HttpServletRequest request,
            @Schema(allowableValues = {"DescribeSpotInstanceRequests", "DescribeInstanceRequests"}, description =
                    "Use a describe instance requests Action and provide a comma separated list of requestId" +
                    " in the legacy request-id parameter or its InstanceRequestId alias for describing instance requests")
            @RequestParam(name = "Action") String action,

            @Schema(description = "comma separated list of request IDs for description when Action describes instance requests. Interchangeable with InstanceRequestId; both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "SpotInstanceRequestId", required = false)
            Set<String> legacyInstanceRequestIds,

            @Schema(description = "Alias for the legacy request-id parameter. Both names are accepted and unioned if both are supplied.")
            @RequestParam(name = "InstanceRequestId", required = false)
            Set<String> instanceRequestIds,

            @Schema(description =
                    "comma separated list of request state. This will be considered with Action=DescribeSpotInstanceRequests or DescribeInstanceRequests.",
                    allowableValues = {"open", "closed", "active", "canceled"})
            @RequestParam(name = "SpotStateFilter", required = false)
            Set<String> stateFilter) {
        Optional<SpotInstanceRequestAction> requestAction =
                SpotInstanceRequestAction.toSpotInstanceRequestAction(
                        action);

        Set<String> requestIds = mergeRequestIds(legacyInstanceRequestIds, instanceRequestIds);
        if (requestIds == null || requestIds.isEmpty()) {
            throw new IcmsBadRequestException(
                    "Either SpotInstanceRequestId or InstanceRequestId must be provided");
        }

        if (requestAction.isPresent() &&
                requestAction.get() == SpotInstanceRequestAction.DESCRIBE_SPOT_INSTANCE_REQUESTS) {
            return ResponseEntity.ok(
                    instanceService.describeAdminInstanceRequests(requestIds, stateFilter));
        } else if (requestAction.isPresent() &&
                requestAction.get() == SpotInstanceRequestAction.DESCRIBE_INSTANCE_REQUESTS) {
            return ResponseEntity.ok(InstanceModelConverter.toGetInstanceRequestsResponse(
                    instanceService.describeAdminInstanceRequests(requestIds, stateFilter)));
        }

        throwExceptionInvalidAction(action, "Invalid", null);
        return ResponseEntity.noContent().build();
    }

    //Validate request and thrown an exception if something is wrong
    private void validateInstancesRequest(
            @NotNull SpotInstanceRequestSchema instanceRequestSchema) {

        if (instanceRequestSchema.getInstanceCount() <= 0) {
            UnifiedErrorException unifiedErrorException = new IcmsHttpUnifiedErrorException(
                    NVCF_INCORRECT_PARAMETER,
                    HttpStatus.BAD_REQUEST,
                    String.format("Instance count must be positive. Provided %d", instanceRequestSchema.getInstanceCount()),
                    toUnifiedErrorData(instanceRequestSchema));

            unifiedErrorReporter.reportAndThrow(unifiedErrorException);
        }

        if (instanceRequestSchema.getAction() == null) {
            throwExceptionInvalidAction(null, "Invalid", toUnifiedErrorData(instanceRequestSchema));
        }

        if (instanceRequestSchema.getAction() != SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES &&
                instanceRequestSchema.getAction() != SpotInstanceRequestAction.REQUEST_SPOT_INSTANCES_FOR_TASK &&
                instanceRequestSchema.getAction() != SpotInstanceRequestAction.REQUEST_INSTANCES &&
                instanceRequestSchema.getAction() != SpotInstanceRequestAction.REQUEST_INSTANCES_FOR_TASK) {
            throwExceptionInvalidAction(instanceRequestSchema.getAction().getRequestAction(), "Not allowed", toUnifiedErrorData(instanceRequestSchema));
        }

        if (instanceRequestSchema.isCacheArtifacts() &&
                StringUtils.isEmpty(instanceRequestSchema.getCacheHandle())) {
            String errMsg =
                    "LaunchSpecification.CacheHandle must be provided when LaunchSpecification.CacheArtifacts is true";
            log.error(errMsg);
            throw new IcmsBadRequestException(errMsg);
        }
    }


    private void throwExceptionInvalidAction(String action, String typeofAction, @Nullable UnifiedErrorData unifiedErrorData) {
        UnifiedErrorException unifiedErrorException = new IcmsHttpUnifiedErrorException(
                NVCF_INCORRECT_PARAMETER, HttpStatus.BAD_REQUEST,
                String.format("%s %s action provided", typeofAction, action), unifiedErrorData);

             unifiedErrorReporter.reportAndThrow(unifiedErrorException);
    }


    /**
     * Wraps a describe-by-deployment result in a ResponseEntity, switching to
     * the prefix-less InstanceRequests shape when UseConciseName=true.
     */
    private static ResponseEntity<?> toDescribeInstancesResponse(
            GetSpotInstanceRequests result, Boolean useConciseName) {
        if (Boolean.TRUE.equals(useConciseName)) {
            return ResponseEntity.ok(InstanceModelConverter.toGetInstanceRequestsResponse(result));
        }
        return ResponseEntity.ok(result);
    }

    /**
     * Returns the union of the two request-id sets, supporting the
     * SpotInstanceRequestId / InstanceRequestId alias. Returns null if both
     * inputs are null (to preserve "not supplied" semantics for callers that
     * distinguish null from empty).
     */
    private static Set<String> mergeRequestIds(Set<String> primary, Set<String> alias) {
        if (primary == null && alias == null) {
            return null;
        }
        Set<String> merged = new HashSet<>();
        if (primary != null) {
            merged.addAll(primary);
        }
        if (alias != null) {
            merged.addAll(alias);
        }
        return merged;
    }

    private Set<String> validateState(Set<String> requestIds, Set<String> givenStates) {
        if ((requestIds == null || requestIds.isEmpty()) &&
                (givenStates == null || givenStates.isEmpty())) {
            // Default to both OPEN and ACTIVE to support state transition feature
            // When feature flag is OFF: only OPEN requests exist (backward compatible)
            // When feature flag is ON: shows both OPEN and ACTIVE requests (expected behavior)
            givenStates = Set.of(SpotInstanceRequestState.OPEN.toString(),
                                 SpotInstanceRequestState.ACTIVE.toString());
        }

        if (givenStates != null && !givenStates.isEmpty()) {
            Set<String> updatedStateFilterList =
                    givenStates.stream().map(String::toLowerCase).collect(Collectors.toSet());

            for (String state : updatedStateFilterList) {
                if (SpotInstanceRequestState.toSpotInstanceRequestState(state).isEmpty()) {
                    throw new IcmsBadRequestException(
                            String.format("'%s' invalid SpotStateFilter provided", state));
                }
            }
            return updatedStateFilterList;
        }

        return Collections.emptySet();
    }
}
