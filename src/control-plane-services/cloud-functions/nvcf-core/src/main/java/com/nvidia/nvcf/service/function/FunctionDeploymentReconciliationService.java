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

import static com.nvidia.nvcf.util.NvcfConstants.UNKNOWN;
import static java.util.stream.Collectors.counting;
import static java.util.stream.Collectors.groupingBy;
import static java.util.stream.Collectors.toMap;
import static java.util.stream.Collectors.toSet;
import static org.apache.commons.lang3.StringUtils.defaultIfBlank;
import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.common.collect.Sets;
import com.nvidia.boot.exceptions.BootResponseException;
import com.nvidia.nvcf.icms.allocator.IcmsAllocatorService;
import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.DeploymentHealthUdt;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.service.instance.InstanceService;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class FunctionDeploymentReconciliationService {
    private static final UUID UNKNOWN_ICMS_REQUEST_ID = new UUID(0, 0);
    private static final String MESG_DEPLOYING_STATUS_UPDATE_FAILED =
            "Function id '%s', version '%s', deployment id '%s': " +
                    "Failed to update deploying function status: %s";
    private static final String MESG_DEPLOYMENT_RECONCILIATION =
            "Account '{}', function id '{}', version '{}', status '{}': {} deployment "
                    + "reconciliation";
    private static final String MESG_TRANSITIONING_STATUS =
            "Function id '{}', version '{}': Transitioning status to {}";
    private static final String MESG_ICMS_STATUS_HISTOGRAM =
            "Function id '{}', version '{}': Histogram of ICMS response statuses {}";
    private static final String MESG_EMPTY_HEALTH_INFO_ERROR_LOG =
            "Missing healthInfo or no errors in healthInfo.errorLog.";
    private static final String MESG_FAILED_TO_MANAGE_ICMS_INSTANCES =
            "Function id '{}', version '{}': Failed to manage instances - {}";
    private static final String MESG_FAILED_TO_ALLOCATE_INSTANCES =
            "Function id '{}', version '{}', deployment id '{}', gpu spec id '{}': "
                    + "Failed to allocate instances, keeping retrieved instance state - {}";
    private static final String MESG_FAILED_TO_UPDATE_FUNCTION_STATUS =
            "Function id '{}', version '{}': Failed to update status - {}";
    private static final String MESG_EXCEEDED_MAX_INSTANCE_COUNT =
            "Function id '{}', version '{}', instance type '{}': Max instance count exceeded by {}";
    private static final String MESG_STILL_DEPLOYING =
            "Function id '{}', version '{}': Still deploying - No errors reported yet";
    private static final String MESG_ISSUES_WITH_SOME_DEPLOYMENT_SPECS =
            "Function id '{}', version '{}': Retrieve deployment and check for errors";

    private final IcmsClient icmsClient;
    private final FunctionDeploymentService functionDeploymentService;
    private final InstanceService instanceService;
    private final IcmsAllocatorService icmsAllocatorService;

    public FunctionDeploymentReconciliationService(
            IcmsClient icmsClient,
            FunctionDeploymentService functionDeploymentService,
            InstanceService instanceService,
            IcmsAllocatorService icmsAllocatorService) {
        this.icmsClient = icmsClient;
        this.functionDeploymentService = functionDeploymentService;
        this.instanceService = instanceService;
        this.icmsAllocatorService = icmsAllocatorService;
    }

    public FunctionEntity reconcile(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        log.debug(MESG_DEPLOYMENT_RECONCILIATION,
                  function.getNcaId(),
                  function.getFunctionId(),
                  function.getFunctionVersionId(),
                  function.getFunctionStatus(),
                  "start");
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        switch (function.getFunctionStatus()) {
            case ACTIVE, DEGRADED, DEGRADING ->
                    reconcileRunningFunction(function, deploymentContext);
            case DEPLOYING ->
                    reconcileDeployingFunction(function, deploymentContext);
            case ERROR, INACTIVE ->
                    log.debug(
                    "Function id '{}', version '{}': Skipping deployment reconciliation for "
                            + "status '{}'",
                    functionId, versionId, function.getFunctionStatus());
        }
        log.debug(MESG_DEPLOYMENT_RECONCILIATION,
                  function.getNcaId(),
                  functionId,
                  versionId,
                  function.getFunctionStatus(),
                  "end");
        return function;
    }

    private void reconcileRunningFunction(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        Map<UUID, Set<Instance>> gpuSpecIdToInstances = new HashMap<>();
        try {
            gpuSpecIdToInstances = manageRunningFunctionInstances(function, deploymentContext);
        } catch (Exception ex) {
            log.error(MESG_FAILED_TO_MANAGE_ICMS_INSTANCES,
                      function.getFunctionId(),
                      function.getFunctionVersionId(),
                      ex.getMessage(),
                      ex);
        }
        updateRunningFunctionStatus(function, deploymentContext, gpuSpecIdToInstances);
    }

    private void reconcileDeployingFunction(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        try {
            updateDeployingFunctionStatus(function, deploymentContext);
        } catch (Exception ex) {
            log.error(MESG_FAILED_TO_UPDATE_FUNCTION_STATUS,
                      function.getFunctionId(),
                      function.getFunctionVersionId(),
                      ex.getMessage(),
                      ex);
            updateDeployingFunctionHealthInfoForError(deploymentContext, ex);
            transitionDeployingFunctionToError(function, deploymentContext.deployment());
        }
    }

    private Map<UUID, Set<Instance>> manageRunningFunctionInstances(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        var gpuSpecs = mapGpuSpecsById(deploymentContext);
        var icmsInstances = getInstancesByDeploymentId(function, deployment.getDeploymentId());
        var gpuSpecIdToInstances = groupInstancesByGpuSpecId(function, icmsInstances);
        updateHealthInfo(deploymentContext, gpuSpecIdToInstances);

        // check running instances in min/max boundaries
        allocateRequiredInstances(function, deployment, gpuSpecIdToInstances, gpuSpecs);

        return gpuSpecIdToInstances;
    }

    private void updateDeployingFunctionStatus(FunctionEntity function,
                                               FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        var gpuSpecs = mapGpuSpecsById(deploymentContext);
        var functionId = function.getFunctionId();
        var versionId = deployment.getKey().getFunctionVersionId();
        var ncaId = function.getNcaId();
        var deploymentId = deployment.getDeploymentId();
        var icmsInstances = getDeployingFunctionInstances(function, deployment);
        var gpuSpecIdToInstances = groupInstancesByGpuSpecId(function, icmsInstances);

        var healthInfo = getDeploymentHealthInfo(gpuSpecIdToInstances);
        var metMinCountForAllSpecs =
                hasMetMinCountForAllSpecs(gpuSpecs, gpuSpecIdToInstances);

        if (metMinCountForAllSpecs) {
            log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, FunctionStatus.ACTIVE);
            functionDeploymentService.transitionFunctionToActive(function, deployment);
            updateHealthInfo(deploymentContext, gpuSpecIdToInstances);
        } else {
            var totalRequiredInstancesCount = gpuSpecs.values().stream()
                    .mapToInt(GpuSpecificationEntity::getMinInstances).sum();
            // as NVCF may allocate more instances over time, ICMS may have more instances
            // then needed for this deployment.
            if (hasEnoughInstancesWithHealthInfo(totalRequiredInstancesCount, icmsInstances)) {
                // Only when all the instances for all the deployment specs are failing to
                // come up, we should update the function's status to ERROR.
                log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, FunctionStatus.ERROR);
                functionDeploymentService.transitionDeployingFunctionToError(function, deployment);
                instanceService.deleteInstances(ncaId, versionId, deploymentId);
            } else {
                // Maybe, the instances are still coming up and one of them might be healthy.
                // Give it more time.
                log.debug(MESG_STILL_DEPLOYING, functionId, versionId);

                // some instances may have come up, but not enough to hit min instance count for
                // all deployment specs. run an allocation to request additional instances if
                // necessary. if no instances have come up for every given gpuSpec let the
                // first batch keep working. there's a good chance there is some error in the
                // inference container, so we don't want to keep trying to create instances if we
                // haven't gotten a single healthy instance of each gpuSpec from the first
                // batch.
                if (hasRunningInstanceGroupsForAllSpecs(gpuSpecs, gpuSpecIdToInstances)) {
                    allocateRequiredInstances(function, deployment, gpuSpecIdToInstances, gpuSpecs);
                }
            }
        }

        if (!healthInfo.isEmpty()) {
            // We know that all instances associated with all/some deployment specs are failing.
            // We can set the health info and update the deployment entity. If the Org Admin
            // retrieves the deployment, they will get the reason for the failure and act on it.
            log.info(MESG_ISSUES_WITH_SOME_DEPLOYMENT_SPECS, functionId, versionId);
            deployment.setHealthInfo(healthInfo);
            functionDeploymentService.save(deploymentContext);
        }
    }

    /**
     * Checks if number of running + starting instances is inside min/max range for each gpu.
     * If the number is outside, allocate/terminate instances to fit requirements
     *
     * @param function             current function
     * @param deployment           current deployment
     * @param gpuSpecIdToInstances ICMS response for this deployment with current
     *                             instances grouped by gpuSpecId
     */
    private void allocateRequiredInstances(FunctionEntity function,
                                           FunctionDeploymentEntity deployment,
                                           Map<UUID, Set<Instance>> gpuSpecIdToInstances,
                                           Map<UUID, GpuSpecificationEntity> gpuSpecs) {
        gpuSpecs.values().forEach(gpuSpec -> {
            // Failing to allocate for one spec (for example when the cluster is out of
            // capacity) says nothing about the instances already running, and must not stop
            // the remaining specs from being reconciled or discard the retrieved state used
            // for the function status update.
            try {
                var instances = getInstancesForGpuSpec(gpuSpec, gpuSpecIdToInstances);

                int currentInstancesCount = (int) instances.stream()
                        .filter(FunctionDeploymentReconciliationService::isStartingOrRunning)
                        .count();
                int currentActiveInstancesCount = (int) instances.stream()
                        .filter(FunctionDeploymentReconciliationService::isRunning)
                        .count();

                if (currentInstancesCount < gpuSpec.getMinInstances()) {
                    // Allocate instances
                    int instancesToSchedule = gpuSpec.getMinInstances() - currentInstancesCount;
                    icmsAllocatorService.scheduleNewInstance(
                            function, deployment.getDeploymentId(), gpuSpec, instancesToSchedule);
                } else if (currentActiveInstancesCount > gpuSpec.getMaxInstances()) {
                    // Delete only RUNNING(active) instances with stable IDs. Excluding pending
                    // instances prevents rollover process from deleting active instances. This
                    // is because instances with no instance-ids are filtered out when trying to
                    // construct the list of instance-ids to send to ICMS for termination. And,
                    // we inadvertently include the instance-ids of active instances in the list
                    // that causes complete downtime for functions with min=max=N till all the
                    // pending instances become active.
                    var deleteCount = currentActiveInstancesCount - gpuSpec.getMaxInstances();
                    var deletableInstances = instances.stream()
                            .filter(FunctionDeploymentReconciliationService::isRunning)
                            .collect(toSet());

                    log.warn(MESG_EXCEEDED_MAX_INSTANCE_COUNT,
                             function.getFunctionId(),
                             deployment.getKey().getFunctionVersionId(),
                             gpuSpec.getGpu(),
                             deleteCount);
                    icmsAllocatorService.deleteInstances(function, deleteCount,
                                                         deletableInstances);
                }
            } catch (Exception ex) {
                log.error(MESG_FAILED_TO_ALLOCATE_INSTANCES,
                          function.getFunctionId(), function.getFunctionVersionId(),
                          deployment.getDeploymentId(),
                          gpuSpec.getKey().getGpuSpecificationId(),
                          ex.getMessage());
            }
        });
    }

    /**
     * Updates ACTIVE, DEGRADING and DEGRADED function statuses
     */
    private void updateRunningFunctionStatus(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext,
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        var deployment = deploymentContext.deployment();
        var gpuSpecs = mapGpuSpecsById(deploymentContext);
        var metMinCountForAllDeploymentSpecs =
                hasMetMinCountForAllSpecs(gpuSpecs, gpuSpecIdToInstances);
        var hasAnyActiveInstances = hasAnyStartingOrRunningInstance(gpuSpecIdToInstances);
        updateFunctionStatus(function, deployment, gpuSpecs, hasAnyActiveInstances,
                             metMinCountForAllDeploymentSpecs);
    }

    private void updateFunctionStatus(
            FunctionEntity function,
            FunctionDeploymentEntity deployment,
            Map<UUID, GpuSpecificationEntity> gpuSpecs,
            boolean hasAnyActiveInstances,
            boolean metMinCountForAllDeploymentSpecs) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var gpuSpecsValues = gpuSpecs.values();
        var isZeroScaled = FunctionStatus.ACTIVE.equals(function.getFunctionStatus())
                && gpuSpecsValues.stream().anyMatch(spec -> spec.getMinInstances() == 0);

        if (!hasAnyActiveInstances && !isZeroScaled) {
            if (!FunctionStatus.DEGRADED.equals(function.getFunctionStatus())) {
                log.info(MESG_TRANSITIONING_STATUS,
                         functionId, versionId, FunctionStatus.DEGRADED);
                functionDeploymentService.transitionFunctionToDegraded(function, deployment);
            }
        } else if (metMinCountForAllDeploymentSpecs) {
            if (!FunctionStatus.ACTIVE.equals(function.getFunctionStatus())) {
                log.info(MESG_TRANSITIONING_STATUS,
                         functionId, versionId, FunctionStatus.ACTIVE);
                functionDeploymentService.transitionFunctionToActive(function, deployment);
            }
        } else if (!FunctionStatus.DEGRADING.equals(function.getFunctionStatus())) {
            log.info(MESG_TRANSITIONING_STATUS,
                     functionId, versionId, FunctionStatus.DEGRADING);
            functionDeploymentService.transitionFunctionToDegrading(function, deployment);
        }
    }

    private void logIcmsResponseHistogram(
            FunctionEntity function,
            Map<UUID, Set<Instance>> icmsInstances) {
        try {
            if (log.isDebugEnabled()) {
                Map<String, Long> histogram = icmsInstances.values()
                        .stream()
                        .flatMap(Set::stream)
                        .map(instance -> "InstanceState=" + (instance.getState() != null
                                ? instance.getState().getName()
                                : "NULL"))
                        .collect(groupingBy(state -> state, counting()));
                log.debug(MESG_ICMS_STATUS_HISTOGRAM,
                          function.getFunctionId(),
                          function.getFunctionVersionId(),
                          histogram);
            }
        } catch (Exception ex) {
            log.error("Histogram building failed - '{}'", ex.getMessage());
        }
    }

    private static Set<DeploymentHealthUdt> getDeploymentHealthInfo(
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        return gpuSpecIdToInstances.keySet().stream()
                .map(gpuSpecId -> {
                    var instances = gpuSpecIdToInstances.get(gpuSpecId);
                    var allWithErrors = instances.stream()
                            .noneMatch(instance -> instance.getState().isStartingOrRunning());

                    if (allWithErrors) {
                        return instances.stream()
                                .findAny()
                                .map(FunctionDeploymentReconciliationService
                                             ::getIcmsRequestHealthInfo)
                                .orElse(null);
                    }
                    return null;
                })
                .filter(Objects::nonNull)
                .collect(toSet());
    }

    private static DeploymentHealthUdt getIcmsRequestHealthInfo(Instance instance) {
        var error = instance.getHealthInfo() != null
                && isNotBlank(instance.getHealthInfo().getErrorLog())
                ? instance.getHealthInfo().getErrorLog()
                : MESG_EMPTY_HEALTH_INFO_ERROR_LOG;
        var provider = instance.getCloudProvider();
        var instanceType = instance.getInstanceType();

        return DeploymentHealthUdt.builder()
                .icmsRequestId(UNKNOWN_ICMS_REQUEST_ID)
                .error(error)
                .instanceType(instanceType)
                .gpu(instanceType)
                .backend(provider)
                .build();
    }

    private void transitionDeployingFunctionToError(
            FunctionEntity function,
            FunctionDeploymentEntity deployment) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, FunctionStatus.ERROR);
        functionDeploymentService.transitionDeployingFunctionToError(function, deployment);
        instanceService.deleteInstances(
                function.getNcaId(), versionId, deployment.getDeploymentId());
    }

    private void updateDeployingFunctionHealthInfoForError(
            FunctionDeploymentContext deploymentContext,
            Exception ex) {
        var error = MESG_DEPLOYING_STATUS_UPDATE_FAILED.formatted(
                deploymentContext.deployment().getFunctionId(),
                deploymentContext.deployment().getKey().getFunctionVersionId(),
                deploymentContext.deployment().getDeploymentId(),
                getErrorDetail(ex));
        var healthInfo = deploymentContext.gpuSpecs().stream()
                .map(gpuSpec -> toDeploymentHealthInfo(gpuSpec, error))
                .collect(toSet());
        if (!healthInfo.isEmpty()) {
            deploymentContext.deployment().setHealthInfo(healthInfo);
            functionDeploymentService.save(deploymentContext);
        }
    }

    private static DeploymentHealthUdt toDeploymentHealthInfo(
            GpuSpecificationEntity gpuSpec,
            String error) {
        return DeploymentHealthUdt.builder()
                .icmsRequestId(UNKNOWN_ICMS_REQUEST_ID)
                .backend(defaultIfBlank(gpuSpec.getBackend(), UNKNOWN))
                .gpu(defaultIfBlank(gpuSpec.getGpu(), UNKNOWN))
                .instanceType(defaultIfBlank(gpuSpec.getInstanceType(), UNKNOWN))
                .error(error)
                .build();
    }

    private static String getErrorDetail(Exception ex) {
        if (ex instanceof BootResponseException bootResponseException) {
            return defaultIfBlank(bootResponseException.getBody().getDetail(), ex.getMessage());
        }
        return defaultIfBlank(ex.getMessage(), ex.getClass().getSimpleName());
    }

    private Map<UUID, GpuSpecificationEntity> mapGpuSpecsById(
            FunctionDeploymentContext deploymentContext) {
        return deploymentContext.gpuSpecs().stream()
                .collect(toMap(entity -> entity.getKey().getGpuSpecificationId(), e -> e));
    }

    private List<Instance> getInstancesByDeploymentId(
            FunctionEntity function,
            UUID deploymentId) {
        return icmsClient.getInstancesByDeploymentId(function.getNcaId(), deploymentId);
    }

    private List<Instance> getDeployingFunctionInstances(
            FunctionEntity function,
            FunctionDeploymentEntity deployment) {
        var deploymentId = deployment.getDeploymentId();
        var icmsInstances = getInstancesByDeploymentId(function, deploymentId);
        if (CollectionUtils.isEmpty(icmsInstances)) {
            // There could be terminated instances with valid error message.
            return icmsClient.getInstancesByDeploymentId(
                    function.getNcaId(), deploymentId, true, true);
        }
        return icmsInstances;
    }

    private Map<UUID, Set<Instance>> groupInstancesByGpuSpecId(
            FunctionEntity function,
            List<Instance> icmsInstances) {
        var gpuSpecIdToInstances = icmsInstances.stream()
                .collect(groupingBy(Instance::getGpuSpecificationId, toSet()));
        logIcmsResponseHistogram(function, gpuSpecIdToInstances);
        return gpuSpecIdToInstances;
    }

    private boolean hasMetMinCountForAllSpecs(
            Map<UUID, GpuSpecificationEntity> gpuSpecs,
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        return gpuSpecs.values()
                .stream()
                .allMatch(spec -> {
                    var runningCount = getInstancesForGpuSpec(spec, gpuSpecIdToInstances)
                            .stream()
                            .filter(FunctionDeploymentReconciliationService::isRunning)
                            .count();
                    return runningCount >= spec.getMinInstances();
                });
    }

    private static Set<Instance> getInstancesForGpuSpec(
            GpuSpecificationEntity gpuSpec,
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        return Optional.ofNullable(
                        gpuSpecIdToInstances.get(gpuSpec.getKey().getGpuSpecificationId()))
                .orElse(Set.of());
    }

    private static boolean hasAnyStartingOrRunningInstance(
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        return gpuSpecIdToInstances.values()
                .stream()
                .flatMap(Set::stream)
                .anyMatch(FunctionDeploymentReconciliationService::isStartingOrRunning);
    }

    private static boolean hasRunningInstanceGroupsForAllSpecs(
            Map<UUID, GpuSpecificationEntity> gpuSpecs,
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        var gpuSpecsWithRunningInstances = gpuSpecIdToInstances.values().stream()
                .filter(Objects::nonNull)
                .filter(set -> !CollectionUtils.isEmpty(set))
                .filter(set -> set.stream().anyMatch(
                        FunctionDeploymentReconciliationService::isRunning))
                .count();
        return gpuSpecsWithRunningInstances >= gpuSpecs.size();
    }

    private static boolean hasEnoughInstancesWithHealthInfo(
            int totalRequiredInstancesCount,
            List<Instance> icmsInstances) {
        return totalRequiredInstancesCount <= icmsInstances.size()
                && icmsInstances.stream()
                        .allMatch(instance -> Objects.nonNull(instance.getHealthInfo()));
    }

    private static boolean isRunning(Instance instance) {
        return instance.getState() != null && instance.getState().isRunning();
    }

    private static boolean isStartingOrRunning(Instance instance) {
        return instance.getState() != null && instance.getState().isStartingOrRunning();
    }

    // If the deployment has been having issues with some specs, check if the latest
    // health info can be used to clear/update the errors/logs in the repository.
    private void updateHealthInfo(
            FunctionDeploymentContext deploymentContext,
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        var deployment = deploymentContext.deployment();
        if (deployment.getHealthInfo() != null && !deployment.getHealthInfo().isEmpty()) {
            var latestHealthInfo = getDeploymentHealthInfo(gpuSpecIdToInstances);
            if (latestHealthInfo != null) {
                var onlyInLatest = Sets.difference(latestHealthInfo, deployment.getHealthInfo());
                var common = Sets.intersection(latestHealthInfo, deployment.getHealthInfo());
                deployment.setHealthInfo(Sets.union(onlyInLatest, common));
                functionDeploymentService.save(deploymentContext);
            }
        }
    }
}
