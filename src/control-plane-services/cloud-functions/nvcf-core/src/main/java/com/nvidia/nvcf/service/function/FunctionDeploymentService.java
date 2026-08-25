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
package com.nvidia.nvcf.service.function;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.ACTIVE;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.BUSY_STATUSES;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEGRADED;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEGRADING;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEPLOYING;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.ERROR;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.INACTIVE;
import static com.nvidia.nvcf.service.function.FunctionMapperService.buildUuidGpuSpecificationDtoMap;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_ACTIVATED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_DEGRADED;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_DEGRADING;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_ERROR;
import static com.nvidia.nvcf.util.NvcfConstants.STATE_INACTIVE;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_ACTIVATE_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DEGRADED_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_DEGRADING_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_ERROR_FUNCTION;
import static com.nvidia.nvcf.util.NvcfConstants.SUMMARY_INACTIVATE_FUNCTION;
import static java.lang.String.format;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.persistence.function.DeploymentBatchWriter;
import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentDto;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateFunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Predicate;
import java.util.stream.Collectors;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionDeploymentService {

    private static final String MESG_FORBIDDEN_DEPLOYMENT_OPERATION =
            "Function id '%s', version '%s': Forbidden to '%s' deployment";

    private static final String MESG_FUNCTION_DEPLOYMENT_OPERATION =
            "Function id '{}', version '{}', deployment '{}': {}";
    private static final String MESG_FUNCTION_OPERATION =
            "Function id '{}', version '{}': {}";
    private static final String MESG_DEPLOYMENT_OPERATION =
            "Deployment id '{}': {}";
    private static final String MESG_FUNCTION_OPERATION_NCAID =
            "Account '{}': {}";
    private static final String MESG_INACTIVE_FUNCTION_DEPLOYED =
            "Function id '%s', version '%s': Function deployment found for INACTIVE function." +
                    " Wait for current deployment to be gracefully completed and removed.";
    private static final String MESG_FUNCTION_DEPLOYED =
            "Function id '%s', version '%s': Status %s, use PATCH endpoint to update "
                    + "gpu-specification";
    private static final String MESG_FUNCTION_ERROR =
            "Function id '%s', version '%s': Function already has a deployment with errors";
    private static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s', version '%s': Not found in account '%s'";
    private static final String MESG_FUNCTION_NOT_BUSY_TO_ACTIVATE =
            "Function id '%s', version '%s': Cannot activate as the status is not in any of " +
                    "[DEPLOYING, DEGRADED, DEGRADING].";
    private static final String MESG_FUNCTION_NOT_BUSY_TO_DEGRADED =
            "Function id '%s', version '%s': Cannot transition to degraded as the status " +
                    "is not ACTIVE or DEGRADING.";
    private static final String MESG_FUNCTION_NOT_BUSY_TO_DEGRADING =
            "Function id '%s', version '%s': Cannot transition to degrading as the status " +
                    "is not ACTIVE or DEGRADED.";
    private static final String MESG_FUNCTION_CANNOT_INACTIVATE =
            "Function id '%s', version '%s': Cannot transition to INACTIVE as the current " +
                    "status is not ERROR.";
    private static final String MESG_FAILED_CREATE_DEPLOYMENT =
            "Function id '%s', version '%s': Failed to deploy, reverting state to '%s': %s";
    private static final String MESG_FAILED_UPDATE_DEPLOYMENT =
            "Function id '%s', version '%s': Failed to update deployment: %s";
    private static final String MESG_FAILED_DELETE_DEPLOYMENT =
            "Function id '%s', version '%s': Failed to cleanly delete deployment - %s";
    private static final String MESG_MISSING_DEPLOYMENT =
            "Function id '%s', version '%s': Missing deployment";
    private static final String MESG_INVALID_STATUS =
            "Function id '%s', version '%s': Invalid status '%s'";
    private static final String MESG_NATS_STREAM_DELETE_FAILED =
            "Function version '{}': Failed to delete NATS stream - {}";
    private static final String MESG_ZERO_SCALE =
            "Function id '{}', version '{}': Zero min instances in deployment specs - " +
                    "Changing status to ACTIVE";
    private static final String MESG_FAILED_TO_INACTIVATE =
            "Function id '{}', version '{}': Failed to inactivate errored " +
                    "function with old deployment {}";

    private final FunctionAuditService functionAuditService;
    private final FunctionsRepository functionsRepository;
    private final FunctionsDeploymentRepository deploymentRepository;
    private final GpuSpecificationService gpuSpecificationService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final DeploymentValidationService deploymentValidationService;
    private final DeploymentBatchWriter deploymentBatchWriter;
    private final FunctionMapperService functionMapperService;
    private final FunctionDeploymentMapperService functionDeploymentMapperService;
    private final FunctionLookupService functionLookupService;
    private final IcmsAllocatorService icmsAllocatorService;
    private final InstanceService instanceService;
    private final JsonMapper jsonMapper;
    private final WorkerNatsService workerNatsService;
    private final RegistryArtifactService artifactService;

    // Function deployment is created by the admin.
    @SneakyThrows
    public FunctionDeploymentDto createFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            FunctionDeploymentRequest deploymentRequest,
            AuditEventPayload.Builder payloadBuilder,
            Predicate<FunctionEntity> authForPrivateFunction) {
        var function = lookupAndValidateNcaId(ncaId, functionId, functionVersionId);
        if (!authForPrivateFunction.test(function)) {
            var mesg = MESG_FORBIDDEN_DEPLOYMENT_OPERATION
                    .formatted(functionId, functionVersionId, "create");
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        var optContext =
                functionDeploymentLookupService.getDeploymentContextByVersionId(functionVersionId);
        var status = function.getFunctionStatus();
        if (status == INACTIVE) {
            if (optContext.isPresent()) {
                // Function with INACTIVE status has a deployment. Graceful undeployment must
                // be in progress.
                String mesg =
                        MESG_INACTIVE_FUNCTION_DEPLOYED.formatted(functionId, functionVersionId);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        } else {
            if (optContext.isEmpty()) {
                var mesg = MESG_MISSING_DEPLOYMENT.formatted(functionId, functionVersionId);
                log.error(mesg);
                throw new IllegalStateException(mesg);
            }

            var mesg = switch (status) {
                case ACTIVE, DEPLOYING, DEGRADING, DEGRADED ->
                        MESG_FUNCTION_DEPLOYED.formatted(functionId, functionVersionId, status);
                case ERROR -> MESG_FUNCTION_ERROR.formatted(functionId, functionVersionId);
                default -> "Invalid status";
            };
            log.info(mesg);
            throw new BadRequestException(mesg);
        }
        deploymentValidationService.validateDeploymentRequest(ncaId, function, deploymentRequest);
        artifactService.validateArtifacts(function);

        var functionJsonBefore = jsonMapper.valueToTree(function);
        var deploymentId = UUID.randomUUID();
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION,
                 functionId, functionVersionId, deploymentId, "Creating deployment");
        FunctionDeploymentEntity deployment;
        List<GpuSpecificationEntity> gpuSpecEntities;

        try {
            var gpuSpecDtoMap = buildUuidGpuSpecificationDtoMap(deploymentRequest);
            deployment = functionMapperService.toFunctionDeploymentEntity(
                    ncaId, functionId, functionVersionId, deploymentId);
            gpuSpecEntities = functionDeploymentMapperService.toGpuSpecificationEntities(
                    deploymentId, ncaId, gpuSpecDtoMap);

            // Start with min instances requested. If minimum is zero, do not deploy instance,
            // just move status to ACTIVE. Autoscaler will pick the function from here and
            // will deploy instances if needed.
            boolean isZeroScaling = gpuSpecEntities.stream()
                    .allMatch(spec -> spec.getMinInstances() == 0);

            gpuSpecEntities.forEach(spec -> {
                gpuSpecificationService.updateInstanceType(ncaId, spec,
                                                           getFunctionContainerType(function));
                if (spec.getMinInstances() > 0) {
                    icmsAllocatorService.scheduleNewInstance(
                            function, deploymentId, spec, spec.getMinInstances());
                }
            });

            // Update deployment status in the db after requesting the first instances
            // so that we don't incorrectly assume the request failed and was cleaned up
            // while scanning for deployments with no ICMS requests. Dual-write to
            // functions_deployment_v2 and gpu_specifications in one batch.
            deploymentBatchWriter.createDeployment(
                    new FunctionDeploymentContext(deployment, gpuSpecEntities));
            if (isZeroScaling) {
                log.info(MESG_ZERO_SCALE, functionId, functionVersionId);
                function.setFunctionStatus(ACTIVE);
            } else {
                function.setFunctionStatus(DEPLOYING);
            }
            functionsRepository.insert(function);
        } catch (Exception ex) {
            var mesg = format(MESG_FAILED_CREATE_DEPLOYMENT,
                              functionId, functionVersionId, status, ex.getMessage());
            log.error(mesg);
            function.setFunctionStatus(status);
            functionsRepository.insert(function);
            deploymentRepository.deleteByKeyFunctionVersionId(functionVersionId);
            deploymentBatchWriter.deleteGpuSpecs(function.getNcaId(), deploymentId);
            deleteFunctionRequestQueue(functionVersionId);
            instanceService.deleteInstances(function.getNcaId(), functionVersionId, deploymentId);

            // If ICMS throws a BadRequestException, then the same type should be preserved so
            // that exception handlers can propagate appropriate status code when a function
            // with an invalid deployment spec is being deployed.
            Exception wrappedException;
            if (ex instanceof BootResponseException) {
                var paramTypes = new Class[] {String.class, Throwable.class};
                wrappedException =
                        ex.getClass().getDeclaredConstructor(paramTypes).newInstance(mesg, ex);
            } else {
                wrappedException = new IllegalStateException(mesg, ex);
            }
            throw wrappedException;
        }

        functionAuditService.auditCreateFunctionDeployment(payloadBuilder, functionJsonBefore,
                                                           function, deployment);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION,
                 functionId, functionVersionId, deploymentId, "Created deployment");

        return functionMapperService.toFunctionDeploymentDto(
                new FunctionDeploymentContext(deployment, gpuSpecEntities),
                function.getFunctionStatus(), function.getFunctionName(),
                gpuSpecificationService.getInstanceTypesQuietly(
                        ncaId, getFunctionContainerType(function)));
    }

    // When a deployment is deleted, the status of the function changes to INACTIVE. Also,
    // the associated queue and the workers are immediately deleted losing both the
    // inflight messages and the ones in the queue.
    public FunctionDto deleteFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            boolean graceful,
            AuditEventPayload.Builder payloadBuilder,
            Predicate<FunctionEntity> authForPrivateFunction) {
        var function = lookupAndValidateNcaId(ncaId, functionId, functionVersionId);
        if (!authForPrivateFunction.test(function)) {
            var mesg = MESG_FORBIDDEN_DEPLOYMENT_OPERATION
                    .formatted(functionId, functionVersionId, "delete");
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        var deployment =
                functionDeploymentLookupService.getDeploymentContextByVersionIdOrThrow(
                        functionVersionId)
                .deployment();
        var functionJsonBefore = jsonMapper.valueToTree(function);

        log.info(MESG_FUNCTION_OPERATION, functionId, functionVersionId, "Deleting deployment");

        try {
            // Mark function as INACTIVE to stop accepting new jobs/tasks for this version.
            function.setFunctionStatus(INACTIVE);
            functionsRepository.insert(function);

            // The current default behavior for deleting a deployment is ungraceful as we whack the
            // queue and the workers. In the future, when we get the opportunity to break backward
            // compatibility, the default behavior for deleting a deployment will be graceful
            // such that the Workers will get to drain the existing messages in the queue will be
            // the default.
            //
            // If it is a either an ungraceful undeployment OR a graceful undeployment with an empty
            // invocation queue, we can forcibly delete the existing deployment.
            if (!graceful || workerNatsService.isQueueDrained(functionVersionId)) {
                forceDeleteFunctionDeployment(functionVersionId);
                functionAuditService.auditDeleteFunctionDeployment(payloadBuilder,
                                                                   functionJsonBefore,
                                                                   function, deployment);
            } else {
                // Only audit Function status update. The deployment will be deleted in the
                // GracefulDeploymentCleanupService and the corresponding audit log will be
                // created from there.
                functionAuditService.auditFunctionUpdate(payloadBuilder, functionJsonBefore,
                                                         function);
            }
        } catch (Exception ex) {
            var mesg = format(MESG_FAILED_DELETE_DEPLOYMENT,
                              functionId, functionVersionId, ex.getMessage());
            log.warn(mesg); // Log and swallow the exception.
        }

        log.info(MESG_FUNCTION_OPERATION,
                 functionId, functionVersionId, "Deleted deployment");
        return functionMapperService.toFunctionDto(function, Optional.empty(), Optional.empty());
    }

    /**
     * Forcefully deletes function's deployment which includes corresponding NATS queues,
     * the Workers, and the entry from the functions_deployment_v2 table.
     *
     * @param functionVersionId Version id of the function whose deployment is to be deleted
     */
    public void forceDeleteFunctionDeployment(UUID functionVersionId) {
        deleteFunctionRequestQueue(functionVersionId);
        var deploymentOpt = functionDeploymentLookupService
                .getDeploymentContextByVersionId(functionVersionId)
                .map(FunctionDeploymentContext::deployment);

        try {
            deploymentOpt.ifPresent(
                    deployment -> deploymentBatchWriter.deleteDeployment(
                            deployment.getNcaId(),
                            functionVersionId,
                            deployment.getDeploymentId()));
        } finally {
            // Terminate instances associated with the function version when the deployment
            // metadata is still available. To avoid orphan deployments (when deployment id was
            // removed from NVCF DB but left in ICMS) we should remove deployment from ICMS
            // disregard was deleting NVCF DB successful or not.
            deploymentOpt.ifPresent(deployment -> {
                try {
                    instanceService.deleteInstances(
                            deployment.getNcaId(), functionVersionId, deployment.getDeploymentId());
                } catch (Exception ex) {
                    log.warn(MESG_FAILED_DELETE_DEPLOYMENT.formatted(deployment.getFunctionId(),
                                                                     functionVersionId,
                                                                     ex.getMessage()));
                }
            });
        }
    }

    public void deleteFunctionRequestQueue(UUID functionVersionId) {
        try {
            workerNatsService.deleteStreams(functionVersionId);
        } catch (Exception ex) {
            // Log and swallow the exception as we want to continue with the rest of the
            // cleanup.
            log.warn(MESG_NATS_STREAM_DELETE_FAILED, functionVersionId, ex.getMessage());
        }
    }

    public FunctionDeploymentDto getFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            Predicate<FunctionEntity> authForPrivateFunction) {
        log.debug(MESG_FUNCTION_OPERATION, functionId, functionVersionId, "Retrieve deployment");

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionIdOrThrow(
                        functionVersionId);
        deploymentValidationService.validateDeployment(deploymentContext.deployment(), ncaId);
        var function = fetchAndValidateFunction(
                functionId, functionVersionId, authForPrivateFunction);

        log.debug(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                  deploymentContext.deployment().getDeploymentId(), "Retrieved deployment");

        return toFunctionDeploymentDto(function, deploymentContext, ncaId);
    }

    public FunctionDeploymentDto getFunctionDeployment(
            String ncaId,
            UUID deploymentId,
            Predicate<FunctionEntity> authForPrivateFunction) {
        log.debug(MESG_DEPLOYMENT_OPERATION, deploymentId, "Retrieve deployment");

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByDeploymentIdOrThrow(
                        deploymentId);
        var deployment = deploymentContext.deployment();
        deploymentValidationService.validateDeployment(deployment, ncaId);

        var functionId = deployment.getFunctionId();
        var functionVersionId = deployment.getKey().getFunctionVersionId();
        var function = fetchAndValidateFunction(
                functionId, functionVersionId, authForPrivateFunction);

        log.debug(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                  deployment.getDeploymentId(), "Retrieved deployment");

        return toFunctionDeploymentDto(function, deploymentContext, ncaId);
    }

    public List<FunctionDeploymentDto> getAllFunctionDeployments(
            String ncaId,
            Predicate<FunctionEntity> authFilterForPrivateFunctions) {
        log.debug(MESG_FUNCTION_OPERATION_NCAID, ncaId, "Retrieve all deployments");
        var allFunctions = functionsRepository.findAllByNcaId(ncaId);
        var allDeploymentsMap =
                functionDeploymentLookupService.getFunctionDeploymentContextByNcaId(ncaId)
                        .collect(Collectors.toMap(
                                context -> context.deployment().getKey().getFunctionVersionId(),
                                context -> context));
        return allFunctions
                .filter(authFilterForPrivateFunctions)
                .filter(func -> BUSY_STATUSES.contains(func.getFunctionStatus())
                        || func.getFunctionStatus() == ERROR)
                .map(func -> {
                    var versionId = func.getFunctionVersionId();
                    return Optional.ofNullable(allDeploymentsMap.get(versionId))
                            .map(deploymentContext ->
                                    toFunctionDeploymentDto(func, deploymentContext, ncaId))
                            .orElse(null);
                })
                .filter(Objects::nonNull)
                .toList();
    }

    public FunctionEntity transitionFunctionToActive(
            @NonNull UUID functionId,
            @NonNull UUID functionVersionId,
            @NonNull UUID deploymentId) {
        var optFunction = functionLookupService.lookupUsingFunctionIdAndVersionId(
                functionId, functionVersionId);
        var function =
                optFunction.filter(
                                func -> BUSY_STATUSES.contains(func.getFunctionStatus()))
                        .orElseThrow(() -> {
                            var mesg = format(MESG_FUNCTION_NOT_BUSY_TO_ACTIVATE, functionId,
                                              functionVersionId);
                            log.error(mesg);
                            return new IllegalArgumentException(mesg);
                        });
        var jsonBefore = jsonMapper.valueToTree(function);

        function.setFunctionStatus(ACTIVE);
        functionsRepository.insert(function);

        var summary = SUMMARY_ACTIVATE_FUNCTION.formatted(functionId, functionVersionId);
        functionAuditService.auditFunctionUpdate(summary, STATE_ACTIVATED, jsonBefore, function);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deploymentId, "Activating");
        return function;
    }

    public FunctionEntity transitionDeployingFunctionToError(
            @NonNull UUID functionId,
            @NonNull UUID functionVersionId,
            @NonNull UUID deploymentId) {
        var function = getDeployingFunctionOrThrow(functionId, functionVersionId);
        var jsonBefore = jsonMapper.valueToTree(function);

        // Once we set the function status to ERROR we'll no longer attempt to
        // clean its deployment. Org Admin should delete the deployment to reset
        // the function's status to ACTIVE.
        function.setFunctionStatus(FunctionStatus.ERROR);
        functionsRepository.insert(function);

        var summary = SUMMARY_ERROR_FUNCTION.formatted(functionId, functionVersionId);
        functionAuditService.auditFunctionUpdate(summary, STATE_ERROR, jsonBefore, function);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deploymentId, "Error");
        return function;
    }

    public FunctionEntity transitionFunctionToDegrading(
            @NonNull UUID functionId,
            @NonNull UUID functionVersionId,
            @NonNull UUID deploymentId) {
        var optFunction = functionLookupService.lookupUsingFunctionIdAndVersionId(
                functionId, functionVersionId);
        var function =
                optFunction.filter(
                                func -> BUSY_STATUSES.contains(func.getFunctionStatus()))
                        .orElseThrow(() -> {
                            var mesg = format(MESG_FUNCTION_NOT_BUSY_TO_DEGRADING, functionId,
                                              functionVersionId);
                            log.error(mesg);
                            return new IllegalArgumentException(mesg);
                        });
        var jsonBefore = jsonMapper.valueToTree(function);

        function.setFunctionStatus(DEGRADING);
        functionsRepository.insert(function);

        var summary = SUMMARY_DEGRADING_FUNCTION.formatted(functionId, functionVersionId);
        functionAuditService.auditFunctionUpdate(summary, STATE_DEGRADING, jsonBefore, function);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deploymentId, "Degrading");
        return function;
    }

    public FunctionEntity transitionFunctionToDegraded(
            @NonNull UUID functionId,
            @NonNull UUID functionVersionId,
            @NonNull UUID deploymentId) {
        var optFunction = functionLookupService.lookupUsingFunctionIdAndVersionId(
                functionId, functionVersionId);
        var function =
                optFunction.filter(
                                func -> BUSY_STATUSES.contains(func.getFunctionStatus()))
                        .orElseThrow(() -> {
                            var mesg = format(MESG_FUNCTION_NOT_BUSY_TO_DEGRADED, functionId,
                                              functionVersionId);
                            log.error(mesg);
                            return new IllegalArgumentException(mesg);
                        });
        var jsonBefore = jsonMapper.valueToTree(function);

        function.setFunctionStatus(DEGRADED);
        functionsRepository.insert(function);

        var summary = SUMMARY_DEGRADED_FUNCTION.formatted(functionId, functionVersionId);
        functionAuditService.auditFunctionUpdate(summary, STATE_DEGRADED, jsonBefore, function);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deploymentId, "Degraded");
        return function;
    }

    private FunctionEntity transitionFunctionToInactive(
            @NonNull UUID functionId,
            @NonNull UUID functionVersionId,
            @NonNull UUID deploymentId) {
        var optFunction = functionLookupService.lookupUsingFunctionIdAndVersionId(
                functionId, functionVersionId);
        var function =
                optFunction.filter(func -> func.getFunctionStatus() == ERROR)
                        .orElseThrow(() -> {
                            var mesg = format(MESG_FUNCTION_CANNOT_INACTIVATE, functionId,
                                              functionVersionId);
                            log.error(mesg);
                            return new IllegalArgumentException(mesg);
                        });
        var jsonBefore = jsonMapper.valueToTree(function);

        function.setFunctionStatus(INACTIVE);
        functionsRepository.insert(function);

        var summary = SUMMARY_INACTIVATE_FUNCTION.formatted(functionId, functionVersionId);
        functionAuditService.auditFunctionUpdate(summary, STATE_INACTIVE, jsonBefore, function);
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deploymentId, "Inactivating");
        return function;
    }

    public FunctionEntity cleanupErroredDeployment(
            FunctionEntity function,
            FunctionDeploymentEntity deployment) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var deploymentId = deployment.getDeploymentId();
        var threshold = Instant.now().minus(Duration.ofDays(7));

        if (deployment.getLastUpdatedAt() == null
                || deployment.getLastUpdatedAt().isBefore(threshold)) {
            log.info("Cleaning errored deployment for function id '{}'," +
                             " version '{}', deployment '{}'",
                     functionId, versionId, deployment.getDeploymentId());
            try {
                transitionFunctionToInactive(functionId, versionId, deploymentId);

                deleteFunctionRequestQueue(versionId);
                deploymentBatchWriter.deleteDeployment(function.getNcaId(), versionId,
                                                       deploymentId);
                instanceService.deleteInstances(function.getNcaId(), versionId, deploymentId);

                functionAuditService.auditDeleteFunctionDeployment(function, deployment);
            } catch (Exception ex) {
                log.error(MESG_FAILED_TO_INACTIVATE,
                          functionId, versionId, deployment.getDeploymentId(), ex);
            }
        }

        return function;
    }

    /**
     * Saves deployment and its GPU spec rows. Uses the list from the record.
     */
    public FunctionDeploymentEntity save(FunctionDeploymentContext deploymentContext) {
        deploymentBatchWriter.updateDeployment(deploymentContext);
        return deploymentContext.deployment();
    }

    private FunctionEntity getDeployingFunctionOrThrow(UUID functionId, UUID functionVersionId) {
        var optFunction = functionLookupService.lookupUsingFunctionIdAndVersionId(
                functionId, functionVersionId);
        return optFunction.filter(function -> function.getFunctionStatus() == DEPLOYING)
                .orElseThrow(() -> {
                    var mesg = format(MESG_FUNCTION_NOT_BUSY_TO_ACTIVATE,
                            functionId, functionVersionId);
                    log.error(mesg);
                    return new IllegalArgumentException(mesg);
                });
    }

    public FunctionDeploymentDto updateFunctionDeployment(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UpdateFunctionDeploymentRequest updateDeploymentRequest,
            AuditEventPayload.Builder payloadBuilder,
            Predicate<FunctionEntity> privateFunctionMatch) {
        var function = lookupAndValidateNcaId(ncaId, functionId, functionVersionId);
        if (!privateFunctionMatch.test(function)) {
            var mesg = MESG_FORBIDDEN_DEPLOYMENT_OPERATION
                    .formatted(functionId, functionVersionId, "update");
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        var status = function.getFunctionStatus();
        if (!BUSY_STATUSES.contains(status)) {
            var mesg = MESG_INVALID_STATUS.formatted(functionId, functionVersionId, status);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionIdOrThrow(
                        functionVersionId);
        var deployment = deploymentContext.deployment();
        var gpuSpecList = new ArrayList<>(deploymentContext.gpuSpecs());
        var functionJsonBefore = jsonMapper.valueToTree(function);
        var deploymentJsonBefore = jsonMapper.valueToTree(deployment);

        if (!deploymentValidationService.validateAndUpdateDeploymentSpecs(
                gpuSpecList, updateDeploymentRequest, functionId, functionVersionId)) {
            return toFunctionDeploymentDto(function, deploymentContext, ncaId);
        }
        artifactService.validateArtifacts(function);

        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deployment.getDeploymentId(), "Updating deployment");

        try {
            deployment.setLastUpdatedAt(Instant.now());
            deploymentBatchWriter.updateDeployment(
                    new FunctionDeploymentContext(deployment, gpuSpecList));
        } catch (Exception ex) {
            var mesg = format(MESG_FAILED_UPDATE_DEPLOYMENT, functionId, functionVersionId,
                              ex.getMessage());
            log.error(mesg);
            throw new IllegalStateException(mesg, ex);
        }

        functionAuditService.auditUpdateFunctionDeployment(payloadBuilder, functionJsonBefore,
                                                           deploymentJsonBefore, function,
                                                           deployment);

        status = function.getFunctionStatus(); // Get updated status
        log.info(MESG_FUNCTION_DEPLOYMENT_OPERATION, functionId, functionVersionId,
                 deployment.getDeploymentId(), "Updated deployment");
        return toFunctionDeploymentDto(
                function, new FunctionDeploymentContext(deployment, gpuSpecList), ncaId);
    }


    private FunctionEntity lookupAndValidateNcaId(
            String ncaId, UUID functionId, UUID functionVersionId) {
        return functionLookupService.lookupUsingAccountIdAndFunctionIdAndVersionIdOrThrow(
                ncaId,
                functionId,
                functionVersionId);
    }

    // Determines if the functions is mixed(Helm+Container) or just Container.
    private static InstanceUsageTypeEnum getFunctionContainerType(FunctionEntity function) {
        if (isNotBlank(function.getContainerImage())) {
            return InstanceUsageTypeEnum.CONTAINER;
        }
        return InstanceUsageTypeEnum.DEFAULT;
    }

    private FunctionEntity fetchAndValidateFunction(
            UUID functionId,
            UUID functionVersionId,
            Predicate<FunctionEntity> authForPrivateFunction) {
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, functionVersionId);
        if (!authForPrivateFunction.test(function)) {
            var mesg = MESG_FORBIDDEN_DEPLOYMENT_OPERATION
                    .formatted(functionId, functionVersionId, "get");
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        return function;
    }

    private FunctionDeploymentDto toFunctionDeploymentDto(
            FunctionEntity func,
            FunctionDeploymentContext deploymentContext,
            String ncaId) {
        var instanceTypes = gpuSpecificationService.getInstanceTypesQuietly(
                ncaId, getFunctionContainerType(func));
        var status = func.getFunctionStatus();
        var name = func.getFunctionName();
        return functionMapperService.toFunctionDeploymentDto(
                deploymentContext, status, name, instanceTypes);
    }
}
