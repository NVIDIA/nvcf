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
package com.nvidia.nvcf.icms.allocator;

import static java.util.stream.Collectors.toSet;

import com.nvidia.nvcf.icms.client.IcmsClient;
import com.nvidia.nvcf.icms.client.IcmsStubService.Instance;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.GpuSpecificationEntity;
import com.nvidia.nvcf.service.metrics.FunctionDeploymentMetricsService;
import com.nvidia.nvcf.service.registry.RegistryArtifactService;
import com.nvidia.nvcf.service.worker.WorkerNotaryService;
import jakarta.validation.constraints.Positive;
import java.util.Comparator;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
public class IcmsAllocatorService {

    private static final String MESG_REQUESTED_ICMS_INSTANCE =
            "Function id '{}', version '{}', ICMS requestId '{}', "
                    + "GPU '{}': Requested '{}' instance";
    private static final String MESG_DELETING_EXTRA_INSTANCES =
            "Function id '{}', version '{}': Deleting extra instances '{}' from full list '{}'";
    private static final String MESG_DELETED_EXTRA_INSTANCES =
            "Function id '{}', version '{}': Deleted extra instances '{}' from full list '{}'";
    private static final String MESG_MISSING_DELETABLE_INSTANCES =
            "Function id '{}', version '{}': No deletable instances";
    private static final String MESG_MISSING_IDS_OF_EXTRA_INSTANCES =
            "Function id '{}', version '{}': No instance ids to delete from full list '{}'";

    private final IcmsClient icmsClient;
    private final boolean allocatorEnabled;
    private final WorkerNotaryService workerNotaryService;

    private final RegistryArtifactService artifactService;
    private final FunctionDeploymentMetricsService functionDeploymentMetricsService;

    public IcmsAllocatorService(
            IcmsClient icmsClient,
            @Value("${nvcf.icms.allocator.enabled:true}") boolean allocatorEnabled,
            RegistryArtifactService artifactService,
            FunctionDeploymentMetricsService functionDeploymentMetricsService,
            WorkerNotaryService workerNotaryService) {
        this.icmsClient = icmsClient;
        this.allocatorEnabled = allocatorEnabled;
        this.artifactService = artifactService;
        this.functionDeploymentMetricsService = functionDeploymentMetricsService;
        this.workerNotaryService = workerNotaryService;
    }

    public UUID scheduleNewInstance(
            FunctionEntity function,
            UUID deploymentId,
            GpuSpecificationEntity gpuSpec,
            @Positive int count) {
        if (!allocatorEnabled) {
            return UUID.randomUUID();
        }
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();
        var gpu = gpuSpec.getGpu();
        var cacheSize = artifactService.getArtifactsSize(function);
        var cacheHandle = artifactService.getCacheHandle(functionId, versionId);
        var secretsAssertionToken = workerNotaryService.issueSecretsAssertion(functionId,
                                                                              versionId);
        var requestId = icmsClient.createInstance(function, deploymentId,
                                                  gpuSpec, count, cacheSize,
                                                  cacheHandle, secretsAssertionToken);
        var ncaId = function.getNcaId();
        var instanceType = gpuSpec.getInstanceType();

        functionDeploymentMetricsService
                .recordFunctionDeploymentInstanceRequest(functionId, versionId, ncaId,
                                                         instanceType, count);
        log.info(MESG_REQUESTED_ICMS_INSTANCE, functionId, versionId, requestId, gpu, count);
        return requestId;
    }

    public void deleteInstances(
            FunctionEntity function,
            int instancesToDelete,
            Set<Instance> deletableInstances) {
        var functionId = function.getFunctionId();
        var versionId = function.getFunctionVersionId();

        if (CollectionUtils.isEmpty(deletableInstances)) {
            log.warn(MESG_MISSING_DELETABLE_INSTANCES, functionId, versionId);
            return;
        }

        var allDeletableInstanceIds = deletableInstances.stream()
                .filter(instance -> instance.getInstanceId() != null)
                .map(Instance::getInstanceId)
                .toList();

        // first terminate starting, then active ordered by creation time, oldest first
        var targetInstances = selectInstancesToTerminate(
                deletableInstances, instancesToDelete);

        var targetInstanceIds =
                targetInstances.stream().map(Instance::getInstanceId).toList();
        if (CollectionUtils.isEmpty(targetInstanceIds)) {
            log.warn(MESG_MISSING_IDS_OF_EXTRA_INSTANCES, functionId, versionId,
                     allDeletableInstanceIds);
            return;
        }
        log.info(MESG_DELETING_EXTRA_INSTANCES, functionId, versionId, targetInstanceIds,
                 allDeletableInstanceIds);
        targetInstanceIds.forEach(instanceId -> icmsClient.deleteInstance(
                function.getNcaId(), instanceId));
        targetInstances
                .forEach(instanceRequest -> {
                    functionDeploymentMetricsService
                            .recordFunctionDeploymentInstanceDelete(
                                    functionId,
                                    versionId,
                                    function.getNcaId(),
                                    instanceRequest.getInstanceType(),
                                    1
                                                                   );
                });
        log.info(MESG_DELETED_EXTRA_INSTANCES, functionId, versionId, targetInstanceIds,
                 allDeletableInstanceIds);
    }

    // Prefers pending(starting) instances for termination before active(running); within each
    // group, oldest first. Since Boolean.FALSE is considered less than Boolean.TRUE in the sort
    // order, pending(starting) instances will be before active(running) instances.
    private static Set<Instance> selectInstancesToTerminate(
            Set<Instance> instances,
            int count) {
        return instances.stream()
                .filter(instance -> instance.getInstanceId() != null)
                .sorted(Comparator
                                .comparing((Instance inst) -> inst.getState() != null
                                        && inst.getState().isRunning())
                                .thenComparing(Instance::getCreateTime))
                .limit(count)
                .collect(toSet());
    }
}
