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

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvcf.persistence.function.DeploymentBatchWriter;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.function.FunctionAuditService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.instance.InstanceService;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
public class GracefulCleanDeploymentTask {

    private static final String MESG_FUNC_STATUS_NOT_INACTIVE =
            "Function id '{}, version '{}': Function is not inactive - No cleaning needed";
    private static final String MESG_CLEANING_NEEDED =
            "Function id '{}, version '{}': Queue is drained - Cleaning needed";
    private static final String MESG_CLEANING_NOT_NEEDED =
            "Function id '{}, version '{}': Queue is not drained - Cleaning not needed yet";
    private static final String MESG_CLEANING_DEPLOYMENT =
            "Function id '{}, version '{}': Clean deployment as queue is drained";

    private final DeploymentBatchWriter deploymentBatchWriter;
    private final InstanceService instanceService;
    private final FunctionDeploymentService functionDeploymentService;
    private final WorkerNatsService workerNatsService;
    private final FunctionAuditService functionAuditService;
    private final boolean enabled;

    public GracefulCleanDeploymentTask(
            InstanceService instanceService,
            @Value("${nvcf.scheduled-tasks.deployment-tasks.graceful-clean-deployment-task"
                    + ".enabled:true}")
            boolean enabled,
            DeploymentBatchWriter deploymentBatchWriter,
            FunctionDeploymentService functionDeploymentService,
            WorkerNatsService workerNatsService,
            FunctionAuditService functionAuditService) {
        this.enabled = enabled;
        this.deploymentBatchWriter = deploymentBatchWriter;
        this.instanceService = instanceService;
        this.functionDeploymentService = functionDeploymentService;
        this.workerNatsService = workerNatsService;
        this.functionAuditService = functionAuditService;
    }

    public FunctionEntity run(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        if (!enabled) {
            log.info("Task for gracefully cleaning the deployment is disabled");
            return function;
        }

        return runUnchecked(function, deploymentContext);
    }

    @VisibleForTesting
    FunctionEntity runUnchecked(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext) {
        var functionDeployment = deploymentContext.deployment();
        log.info("Task for gracefully cleaning the deployment is enabled");
        var functionId = functionDeployment.getFunctionId();
        var versionId = functionDeployment.getKey().getFunctionVersionId();

        if (function.getFunctionStatus() != FunctionStatus.INACTIVE) {
            log.info(MESG_FUNC_STATUS_NOT_INACTIVE, functionId, versionId);
            return function;
        }

        if (isQueueDrainedForInactiveDeployedFunction(functionDeployment)) {
            cleanDeploymentForInactiveFunctionWithQueueDrained(function, functionDeployment);
        }
        return function;
    }

    // Verify if the queue has drained for the specified function in the functions_deployment_v2
    // table with INACTIVE status.
    private boolean isQueueDrainedForInactiveDeployedFunction(
            FunctionDeploymentEntity functionDeployment) {
        var functionId = functionDeployment.getFunctionId();
        var versionId = functionDeployment.getKey().getFunctionVersionId();

        if (workerNatsService.isQueueDrained(versionId)) {
            log.info(MESG_CLEANING_NEEDED, functionId, versionId);
            return true;
        }
        log.info(MESG_CLEANING_NOT_NEEDED, functionId, versionId);
        return false;
    }

    // Delete the queue, the Workers, and the deployment for the specified function in the
    // functions_deployment_v2 table with INACTIVE status and drained queue.
    private void cleanDeploymentForInactiveFunctionWithQueueDrained(
            FunctionEntity function,
            FunctionDeploymentEntity functionDeployment) {
        var functionId = functionDeployment.getFunctionId();
        var versionId = functionDeployment.getKey().getFunctionVersionId();

        log.info(MESG_CLEANING_DEPLOYMENT, functionId, versionId);

        // Delete function-specific queue, Workers, and the entry in the deployment table
        // and gpu_specifications (dual-write batch).
        instanceService.deleteInstances(function.getNcaId(), versionId,
                                        functionDeployment.getDeploymentId());
        functionDeploymentService.deleteFunctionRequestQueue(versionId);
        deploymentBatchWriter.deleteDeployment(function.getNcaId(), versionId,
                                               functionDeployment.getDeploymentId());

        functionAuditService.auditDeleteFunctionDeployment(function, functionDeployment);
    }

}
