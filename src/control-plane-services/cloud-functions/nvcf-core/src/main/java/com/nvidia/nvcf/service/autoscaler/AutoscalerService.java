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
package com.nvidia.nvcf.service.autoscaler;

import static com.nvidia.nvcf.service.autoscaler.AutoscalerServiceHelper.buildNoScalingNeededResponse;
import static com.nvidia.nvcf.service.autoscaler.AutoscalerServiceHelper.buildNotScalableResponse;
import static com.nvidia.nvcf.util.NvcfUtils.parseUuid;
import static java.util.stream.Collectors.groupingBy;
import static java.util.stream.Collectors.joining;
import static java.util.stream.Collectors.toSet;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.proto.AutoscalerRequest;
import com.nvidia.nvcf.proto.AutoscalerResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import java.util.HashMap;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

/**
 * Autoscaler implementation that aggregates instance counts across all GPU specs
 * in a deployment and scales while respecting per-spec min/max bounds. During
 * termination, the implementation prioritizes terminating instances that are still
 * pending/starting over instances that are already active/running.
 */
@Slf4j
@Service
public class AutoscalerService {
    private static final String MESG_AUTOSCALER_REQUEST =
            "Function id '{}', version '{}': Request to autoscale function to {} instances";
    private static final String MESG_FUNCTION_NOT_ACTIVE =
            "Function id '{}', version '{}', deployment id '{}': " +
                    "Function is not ACTIVE: {}";
    private static final String MESG_INSTANCES_OUT_OF_RANGE =
            "Function id '{}',  version '{}', deployment id '{}': " +
                    "Function is not scalable as number of instances " +
                    "is not within min and max range.";
    private static final String MESG_CURRENT_MATCH_REQUEST =
            "Function id '{}',  version '{}', deployment id '{}': " +
                    "No scaling needed as number of instances match required.";
    private static final String MESG_NO_SPACE_FOR_SCALING =
            "Function id '{}', version '{}', deployment id '{}': " +
                    "There is no space for scaling, all instances reached there min/max limits.";
    private static final String MESG_DEPLOYMENT_ID_MISMATCH =
            "Function id '%s', version id '%s': deployment id mismatch: expected '%s', got '%s'";
    private static final String MESG_GPU_ID_NOT_FOUND =
            "Function id '%s', version id '%s': deployment does not contain gpu spec id '%s'";
    private static final String MESG_INVALID_REQUIRED_INSTANCES =
            "Function id '%s', version id '%s': " +
                    "requiredNumberOfInstances must be greater than or equal to 0, got '%d'";
    private static final String MESG_DEPLOYMENT_SNAPSHOT =
            "Account '{}', function id '{}', version '{}', deployment id '{}', required '{}: " +
                    "Deployment Snapshot '{}'.";
    private static final String MESG_FAILED_TO_APPLY_SCALING_DELTA =
            "Function id '{}', version '{}', deployment id '{}', gpu spec id '{}': "
                    + "Failed to {} instances for autoscaler request, propagating error - {}";

    private final IcmsClient icmsClient;
    private final IcmsAllocatorService icmsAllocatorService;
    private final FunctionLookupService functionLookupService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;

    public AutoscalerService(
            IcmsClient icmsClient,
            IcmsAllocatorService icmsAllocatorService,
            FunctionLookupService functionLookupService,
            FunctionDeploymentLookupService functionDeploymentLookupService) {
        this.icmsClient = icmsClient;
        this.icmsAllocatorService = icmsAllocatorService;
        this.functionLookupService = functionLookupService;
        this.functionDeploymentLookupService = functionDeploymentLookupService;
    }

    // Aggregate instance counts across all GPU specs in a deployment.
    //
    // aggregateMinInstanceCount: sum of minInstances from every GPU spec in the deployment
    // aggregateMaxInstanceCount: sum of maxInstances from every GPU spec in the deployment
    // aggregateActiveInstanceCount: sum of instances in state "running" across all GPU specs
    // aggregatePendingInstanceCount: sum of instances in state "starting" across all GPU specs
    public record AggregateInstanceCountSnapshot(
            int aggregateMinInstanceCount,
            int aggregateMaxInstanceCount,
            int aggregateActiveInstanceCount,
            int aggregatePendingInstanceCount) {

        public int aggregateTotalInstanceCount() {
            return aggregateActiveInstanceCount + aggregatePendingInstanceCount;
        }
    }

    // Per-GPU-spec instance counts and instances for allocation/termination decisions.
    private record GpuSpecSnapshot(
            UUID gpuSpecId,
            int minInstanceCount,
            int maxInstanceCount,
            int activeInstanceCount,
            int pendingInstanceCount,
            Set<Instance> instances) {

        int totalInstanceCount() {
            return activeInstanceCount + pendingInstanceCount;
        }
    }

    // Snapshot of deployment instance state: aggregate counts and per-spec data.
    private record DeploymentSnapshot(
            UUID deploymentId,
            AggregateInstanceCountSnapshot aggregateInstanceCountSnapshot,
            Map<UUID, GpuSpecSnapshot> gpuSpecIdToSnapshotMap) {

        Map<UUID, Set<Instance>> gpuSpecIdToInstancesMap() {
            var map = new HashMap<UUID, Set<Instance>>();
            gpuSpecIdToSnapshotMap().forEach((id, spec) -> map.put(id, spec.instances()));
            return Map.copyOf(map);
        }
    }

    public AutoscalerResponse scaleInstances(AutoscalerRequest request) {
        var functionId = parseUuid(
                "functionId", request.getFunctionId(),
                request.getFunctionId(), request.getFunctionVersionId());
        var versionId = parseUuid(
                "functionVersionId", request.getFunctionVersionId(),
                request.getFunctionId(), request.getFunctionVersionId());
        var function = functionLookupService.lookupUsingFunctionIdAndVersionIdOrThrow(
                functionId, versionId);
        return scaleInstances(function, request);
    }

    private AutoscalerResponse scaleInstances(
            FunctionEntity function,
            AutoscalerRequest request) {
        var requiredNumberOfInstances = request.getRequiredNumberOfInstances();
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        log.info(MESG_AUTOSCALER_REQUEST, functionId, versionId, requiredNumberOfInstances);

        var deploymentContext =
                functionDeploymentLookupService.getDeploymentContextByVersionIdOrThrow(versionId);
        var deployment = deploymentContext.deployment();
        var deploymentId = deployment.getDeploymentId();

        validateAutoscalerRequest(deploymentContext, request);

        var functionStatus = FunctionStatusEnum.fromText(function.getFunctionStatus().toString());
        var deploymentSnapshot = computeDeploymentSnapshot(function, deploymentContext);
        var gpuSpecIdToInstanceMap = deploymentSnapshot.gpuSpecIdToInstancesMap();

        logCurrentDeploymentSnapshotIfDebug(function, deployment, deploymentSnapshot,
                                            requiredNumberOfInstances);

        var optNotScalableResponse = checkIfFunctionIsAutoscaleable(function, deployment,
                                                                    deploymentSnapshot,
                                                                    gpuSpecIdToInstanceMap);
        if (optNotScalableResponse.isPresent()) {
            return optNotScalableResponse.get();
        }

        var aggregateSnapshot = deploymentSnapshot.aggregateInstanceCountSnapshot();
        var targetInstanceCount = clampTargetToRange(requiredNumberOfInstances, aggregateSnapshot);
        var currentTotalCount = aggregateSnapshot.aggregateTotalInstanceCount();
        var deltaInstanceCount = targetInstanceCount - currentTotalCount;
        var optNoScalingNeededResponse = checkIfScalingIsNeeded(function, deployment,
                                                                deltaInstanceCount,
                                                                gpuSpecIdToInstanceMap);
        if (optNoScalingNeededResponse.isPresent()) {
            return optNoScalingNeededResponse.get();
        }

        var gpuSpecIdToScalingDeltaMap = buildDistributionMap(request,
                                                              deploymentContext,
                                                              deploymentSnapshot,
                                                              deltaInstanceCount);
        log.debug("Function id '{}', version '{}', deployment id '{}': Scaling delta map: '{}'",
                  functionId, versionId, deploymentId, gpuSpecIdToScalingDeltaMap);

        if (gpuSpecIdToScalingDeltaMap.isEmpty()) {
            log.debug(MESG_NO_SPACE_FOR_SCALING, functionId, versionId, deploymentId);
            return buildNoScalingNeededResponse(gpuSpecIdToInstanceMap, functionStatus);
        }

        applyScalingDeltas(function, deploymentContext, deploymentSnapshot,
                           gpuSpecIdToScalingDeltaMap);
        return buildAutoScalingResponse(gpuSpecIdToScalingDeltaMap,
                                        gpuSpecIdToInstanceMap,
                                        functionStatus);
    }

    private void applyScalingDeltas(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext,
            DeploymentSnapshot deploymentSnapshot,
            Map<UUID, Integer> gpuSpecIdToScalingDeltaMap) {
        var deploymentId = deploymentContext.deployment().getDeploymentId();
        var gpuSpecs = deploymentContext.gpuSpecs();
        for (var entry : gpuSpecIdToScalingDeltaMap.entrySet()) {
            var gpuSpecId = entry.getKey();
            int specScalingDelta = entry.getValue();
            var specSnapshot = deploymentSnapshot.gpuSpecIdToSnapshotMap().get(gpuSpecId);
            try {
                if (specScalingDelta > 0) {
                    var gpuSpec = gpuSpecs.stream()
                            .filter(s -> s.getKey().getGpuSpecificationId().equals(gpuSpecId))
                            .findFirst()
                            .orElse(null);
                    icmsAllocatorService.scheduleNewInstance(function, deploymentId, gpuSpec,
                                                             specScalingDelta);
                } else {
                    icmsAllocatorService.deleteInstances(function, Math.abs(specScalingDelta),
                                                         specSnapshot.instances());
                }
            } catch (RuntimeException ex) {
                var action = specScalingDelta > 0 ? "allocate" : "delete";
                log.error(MESG_FAILED_TO_APPLY_SCALING_DELTA,
                          function.getFunctionId(), function.getFunctionVersionId(),
                          deploymentId, gpuSpecId, action, ex.getMessage());
                throw ex;
            }
        }
    }

    // Populates aggregate and per-spec instance counts from gpuSpecs and instances
    // (active = "running", pending = "starting"), grouped by gpu-spec-id.
    private DeploymentSnapshot computeDeploymentSnapshot(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        var gpuSpecs = deploymentContext.gpuSpecs();
        var icmsInstances = icmsClient.getInstancesByDeploymentId(
                function.getNcaId(), deployment.getDeploymentId());
        var instancesByGpuSpec = icmsInstances.stream()
                .filter(inst -> inst.getState() != null && inst.getState().isStartingOrRunning())
                .collect(groupingBy(Instance::getGpuSpecificationId, toSet()));

        var aggregateMinInstanceCount = 0;
        var aggregateMaxInstanceCount = 0;
        var aggregateActiveInstanceCount = 0;
        var aggregatePendingInstanceCount = 0;
        var gpuSpecIdToSnapshotMap = new HashMap<UUID, GpuSpecSnapshot>();

        for (var gpuSpec : gpuSpecs) {
            var gpuSpecId = gpuSpec.getKey().getGpuSpecificationId();
            var instances = instancesByGpuSpec.getOrDefault(gpuSpecId, Set.of());
            var active = (int) instances.stream()
                    .filter(i -> i.getState() != null && i.getState().isRunning())
                    .count();
            var pending = (int) instances.stream()
                    .filter(i -> i.getState() != null && i.getState().isStarting())
                    .count();

            aggregateMinInstanceCount += gpuSpec.getMinInstances();
            aggregateMaxInstanceCount += gpuSpec.getMaxInstances();
            aggregateActiveInstanceCount += active;
            aggregatePendingInstanceCount += pending;
            gpuSpecIdToSnapshotMap.put(gpuSpecId, new GpuSpecSnapshot(gpuSpecId,
                                                                      gpuSpec.getMinInstances(),
                                                                      gpuSpec.getMaxInstances(),
                                                                      active, pending, instances));
        }

        var aggregateInstanceCountSnapshot = new AggregateInstanceCountSnapshot(
                aggregateMinInstanceCount, aggregateMaxInstanceCount,
                aggregateActiveInstanceCount, aggregatePendingInstanceCount);
        return new DeploymentSnapshot(
                deployment.getDeploymentId(),
                aggregateInstanceCountSnapshot,
                Map.copyOf(gpuSpecIdToSnapshotMap));
    }

    private static AutoscalerResponse buildAutoScalingResponse(
            Map<UUID, Integer> gpuSpecIdToScalingDeltaMap,
            Map<UUID, Set<Instance>> gpuId2InstanceMap,
            FunctionStatusEnum functionStatus) {
        int requestedDelta = gpuSpecIdToScalingDeltaMap.values().stream().mapToInt(i -> i).sum();
        if (requestedDelta > 0) {
            return AutoscalerServiceHelper.buildScalingUpResponse(requestedDelta, gpuId2InstanceMap,
                                                                  functionStatus);
        }
        return AutoscalerServiceHelper.buildScalingDownResponse(
                Math.abs(requestedDelta), gpuId2InstanceMap, functionStatus);
    }

    private static void logCurrentDeploymentSnapshotIfDebug(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            DeploymentSnapshot deploymentSnapshot,
            int requiredNumberOfInstances) {
        if (log.isDebugEnabled()) {
            var functionId = function.getFunctionId();
            var versionId = function.getFunctionVersionId();
            var deploymentId = deployment.getDeploymentId();
            var ncaId = deployment.getNcaId();
            log.debug(MESG_DEPLOYMENT_SNAPSHOT, ncaId, functionId, versionId, deploymentId,
                      requiredNumberOfInstances,
                      stringifyDeploymentSnapshot(deploymentSnapshot));
        }
    }

    private static String stringifyDeploymentSnapshot(DeploymentSnapshot snapshot) {
        return snapshot.gpuSpecIdToSnapshotMap().entrySet().stream()
                .map(entry -> ("GpuSpecId: '%s', Min: '%d', Max: '%d', Active: '%d', Pending: '%d'")
                        .formatted(
                                entry.getKey(),
                                entry.getValue().minInstanceCount(),
                                entry.getValue().maxInstanceCount(),
                                entry.getValue().activeInstanceCount(),
                                entry.getValue().pendingInstanceCount()))
                .collect(joining("; "));
    }

    // Clamp requested instances to [aggregateMinInstanceCount, aggregateMaxInstanceCount].
    private static int clampTargetToRange(
            int requestedNumberOfInstances,
            AggregateInstanceCountSnapshot aggregate) {
        if (requestedNumberOfInstances <= 0
                || requestedNumberOfInstances < aggregate.aggregateMinInstanceCount()) {
            return aggregate.aggregateMinInstanceCount();
        }
        return Math.min(requestedNumberOfInstances, aggregate.aggregateMaxInstanceCount());
    }

    // Build map from GPU spec ID to number of instances to allocate (positive)
    // or terminate (negative). Respects per-spec min/max; if request targets a single gpuSpecId,
    // only that spec is scaled.
    private static Map<UUID, Integer> buildDistributionMap(
            AutoscalerRequest request,
            FunctionDeploymentContext deploymentContext,
            DeploymentSnapshot snapshot,
            int deltaInstanceCount) {
        var gpuSpecs = deploymentContext.gpuSpecs();
        var result = new HashMap<UUID, Integer>();
        UUID filterGpuSpecId = request.hasGpuSpecificationId()
                ? UUID.fromString(request.getGpuSpecificationId()) : null;
        int remainingDelta = deltaInstanceCount;

        for (var gpuSpec : gpuSpecs) {
            if (remainingDelta == 0) {
                break;
            }
            var gpuSpecId = gpuSpec.getKey().getGpuSpecificationId();
            if (filterGpuSpecId != null && !filterGpuSpecId.equals(gpuSpecId)) {
                continue;
            }
            var specSnapshot = snapshot.gpuSpecIdToSnapshotMap().get(gpuSpecId);
            if (specSnapshot == null) {
                continue;
            }
            int specTotal = specSnapshot.totalInstanceCount();
            int min = specSnapshot.minInstanceCount();
            int max = specSnapshot.maxInstanceCount();

            if (specTotal + remainingDelta >= min && specTotal + remainingDelta <= max) {
                result.put(gpuSpecId, remainingDelta);
                remainingDelta = 0;
            } else if (remainingDelta > 0) {
                int allocationCount = Math.min(max - specTotal, remainingDelta);
                if (allocationCount > 0) {
                    result.put(gpuSpecId, allocationCount);
                    remainingDelta -= allocationCount;
                }
            } else {
                int terminationCount = Math.min(specTotal - min, -remainingDelta);
                if (terminationCount > 0) {
                    result.put(gpuSpecId, -terminationCount);
                    remainingDelta += terminationCount;
                }
            }
        }
        return result;
    }

    // Returns true if ALL the GPU spec snapshots, the total instance count(active+pending)
    // is within the [minInstanceCount, maxInstanceCount] range. In other words, a function
    // is autoscaleable, if ALL the GPU spec snapshots meet the following condition:
    //     totalInstanceCount(activeInstanceCount + pendingInstanceCount) is gte minInstanceCount
    //         AND
    //     totalInstanceCount(activeInstanceCount + pendingInstanceCount) is lte maxInstanceCount
    // Otherwise, false.
    private static boolean isAutoscaleable(DeploymentSnapshot snapshot) {
        return snapshot.gpuSpecIdToSnapshotMap().values().stream().allMatch(spec -> {
            int total = spec.totalInstanceCount();
            return total >= spec.minInstanceCount() && total <= spec.maxInstanceCount();
        });
    }

    // Returns the response to use when the function is not autoscaleable (e.g. not active or
    // instance counts out of range); empty if the function is autoscaleable.
    private static Optional<AutoscalerResponse> checkIfFunctionIsAutoscaleable(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            DeploymentSnapshot deploymentSnapshot,
            Map<UUID, Set<Instance>> gpuId2InstanceMap) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var deploymentId = deployment.getDeploymentId();
        var funcStatus = FunctionStatusEnum.fromText(function.getFunctionStatus().toString());

        if (FunctionStatusEnum.ACTIVE != funcStatus) {
            log.debug(MESG_FUNCTION_NOT_ACTIVE, functionId, versionId, deploymentId, funcStatus);
            return Optional.of(buildNotScalableResponse(gpuId2InstanceMap, funcStatus));
        }
        if (!isAutoscaleable(deploymentSnapshot)) {
            log.debug(MESG_INSTANCES_OUT_OF_RANGE, functionId, versionId, deploymentId);
            return Optional.of(buildNotScalableResponse(gpuId2InstanceMap, funcStatus));
        }
        return Optional.empty();
    }

    // Returns a no-scaling-needed response if target or requested instance count for the
    // function already matches current total instance count -- meaning delta is zero; otherwise
    // return an empty optional.
    private static Optional<AutoscalerResponse> checkIfScalingIsNeeded(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            int deltaInstanceCount,
            Map<UUID, Set<Instance>> gpuId2InstanceMap) {
        if (deltaInstanceCount == 0) {
            var functionId = function.getFunctionId();
            var versionId = function.getFunctionVersionId();
            var deploymentId = deployment.getDeploymentId();
            var status = FunctionStatusEnum.fromText(function.getFunctionStatus().toString());
            log.debug(MESG_CURRENT_MATCH_REQUEST, functionId, versionId, deploymentId);
            return Optional.of(buildNoScalingNeededResponse(gpuId2InstanceMap, status));
        }
        return Optional.empty();
    }

    private static void validateAutoscalerRequest(
            FunctionDeploymentContext deploymentContext,
            AutoscalerRequest request) {
        var deployment = deploymentContext.deployment();
        var functionId = deployment.getFunctionId();
        var versionId = deployment.getKey().getFunctionVersionId();

        if (request.getRequiredNumberOfInstances() < 0) {
            var mesg = MESG_INVALID_REQUIRED_INSTANCES.formatted(
                    functionId, versionId, request.getRequiredNumberOfInstances());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var gpuSpecs = deploymentContext.gpuSpecs();
        var deploymentId =
                parseOptionalUuid("deploymentId", request.hasDeploymentId(),
                                  request.getDeploymentId(), functionId, versionId);
        if (deploymentId != null && !deployment.getDeploymentId().equals(deploymentId)) {
            var mesg = MESG_DEPLOYMENT_ID_MISMATCH.formatted(
                    functionId,
                    versionId,
                    deployment.getDeploymentId(),
                    deploymentId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        var gpuSpecificationId =
                parseOptionalUuid("gpuSpecificationId", request.hasGpuSpecificationId(),
                                  request.getGpuSpecificationId(), functionId, versionId);
        if (gpuSpecificationId != null
                && gpuSpecs.stream()
                .noneMatch(s -> s.getKey().getGpuSpecificationId().equals(gpuSpecificationId))) {
            var mesg = MESG_GPU_ID_NOT_FOUND.formatted(
                    functionId,
                    versionId,
                    gpuSpecificationId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private static UUID parseOptionalUuid(
            String fieldName,
            boolean hasValue,
            String value,
            Object functionId,
            Object versionId) {
        return hasValue ? parseUuid(fieldName, value, functionId, versionId) : null;
    }
}
