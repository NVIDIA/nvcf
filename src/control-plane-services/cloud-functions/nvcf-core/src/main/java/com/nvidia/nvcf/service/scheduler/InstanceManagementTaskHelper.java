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
package com.nvidia.nvcf.service.scheduler;

import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.ACTIVE;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEGRADED;
import static com.nvidia.nvcf.persistence.function.entity.FunctionStatus.DEGRADING;
import static java.util.stream.Collectors.counting;
import static java.util.stream.Collectors.groupingBy;
import static java.util.stream.Collectors.toSet;

import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.DeploymentHealthUdt;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class InstanceManagementTaskHelper {
    static final String MESG_TRANSITIONING_STATUS =
            "Function id '{}', version '{}': Transitioning status to {}";

    private static final String MESG_ICMS_STATUS_HISTOGRAM =
            "Function id '{}', version '{}': Histogram of ICMS response statuses {}";
    private static final String MESG_EMPTY_HEALTH_INFO_ERROR_LOG =
            "Missing healthInfo or no errors in healthInfo.errorLog.";

    private final FunctionDeploymentService functionDeploymentService;

    public InstanceManagementTaskHelper(
            FunctionDeploymentService functionDeploymentService) {
        this.functionDeploymentService = functionDeploymentService;
    }

    public void logIcmsResponseHistogram(
            FunctionEntity function,
            Map<UUID, Set<Instance>> icmsInstances) {
        try {
            if (log.isDebugEnabled()) {
                Map<String, Long> histoMap = icmsInstances.values()
                        .stream()
                        .flatMap(Set::stream)
                        .map(instance -> "InstanceState=" + (instance.getState() != null
                                ? instance.getState().getName()
                                : "NULL"))
                        .collect(groupingBy(state -> state, counting()));
                log.debug(MESG_ICMS_STATUS_HISTOGRAM, function.getFunctionId(),
                          function.getFunctionVersionId(), histoMap);
            }
        } catch (Exception e) {
            log.error("Histogram building failed - '{}'", e.getMessage());
        }
    }

    void updateFunctionStatus(FunctionEntity function,
                              FunctionDeploymentEntity deployment,
                              Map<UUID, GpuSpecificationEntity> gpuSpecs,
                              boolean hasAnyActiveInstances,
                              boolean metMinCountForAllDeploymentSpecs) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var gpuSpecsValues = gpuSpecs.values();
        var isZeroScaled = ACTIVE.equals(function.getFunctionStatus())
                && gpuSpecsValues.stream()
                .anyMatch(spec -> spec.getMinInstances() == 0);

        if (!hasAnyActiveInstances && !isZeroScaled) {
            // no active instances, transition to DEGRADED
            if (!DEGRADED.equals(function.getFunctionStatus())) {
                log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, DEGRADED);
                functionDeploymentService.transitionFunctionToDegraded(
                        functionId, versionId, deployment.getDeploymentId());
            }
        } else {
            // has min required instances, transitioning to ACTIVE
            if (metMinCountForAllDeploymentSpecs) {
                if (!ACTIVE.equals(function.getFunctionStatus())) {
                    log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, ACTIVE);
                    functionDeploymentService.transitionFunctionToActive(
                            functionId, versionId, deployment.getDeploymentId());
                }
            } else {
                // there are some active instances, less than required, DEGRADING
                if (!DEGRADING.equals(function.getFunctionStatus())) {
                    log.info(MESG_TRANSITIONING_STATUS, functionId, versionId, DEGRADING);
                    functionDeploymentService.transitionFunctionToDegrading(
                            functionId, versionId, deployment.getDeploymentId());
                }
            }
        }
    }

    static Set<DeploymentHealthUdt> getDeploymentHealthInfo(
            Map<UUID, Set<Instance>> gpuSpecIdToInstances) {
        return gpuSpecIdToInstances.keySet().stream()
                .map(gpuSpecId -> {
                    var instances = gpuSpecIdToInstances.get(gpuSpecId);
                    var allWithErrors = instances
                            .stream()
                            .noneMatch(instance -> instance.getState().isStartingOrRunning());

                    if (allWithErrors) {
                        // Return DeploymentHealthUdt corresponding to the deployment spec
                        // where all the instances are having issues coming up. Just pick any
                        // instance to get the error log.
                        return instances
                                .stream()
                                .findAny()
                                .map(InstanceManagementTaskHelper::getIcmsRequestHealthInfo)
                                .orElse(null);
                    }
                    return null;
                })
                .filter(Objects::nonNull)
                .collect(toSet());
    }

    private static DeploymentHealthUdt getIcmsRequestHealthInfo(
            Instance instance) {
        var error = (instance.getHealthInfo() != null
                && StringUtils.isNotBlank(instance.getHealthInfo().getErrorLog()))
                ? instance.getHealthInfo().getErrorLog()
                : MESG_EMPTY_HEALTH_INFO_ERROR_LOG;
        var provider = instance.getCloudProvider();
        var instanceType = instance.getInstanceType();

        return DeploymentHealthUdt.builder()
                // fake ICMS request id
                .icmsRequestId(new UUID(0, 0))
                .error(error)
                .instanceType(instanceType)
                .gpu(instanceType)
                .backend(provider)
                .build();
    }
}
