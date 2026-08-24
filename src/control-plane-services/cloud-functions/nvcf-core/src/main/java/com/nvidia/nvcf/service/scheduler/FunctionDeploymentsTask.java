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

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_STATUS;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_VERSION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvcf.configuration.scheduler.FunctionDeploymentsTaskProperties;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionDeploymentReconciliationService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.GracefulDeploymentCleanupService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.core.annotation.Timed;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import jakarta.annotation.Nullable;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(
        name = "nvcf.scheduler.enabled", havingValue = "true", matchIfMissing = true)
public class FunctionDeploymentsTask implements AutoCloseable {

    private static final String MESG_FUNCTION_DEPLOYMENTS_DURATION =
            "Region '{}': Function deployments task started at '{}', ended at '{}', spent '{}'";
    private static final String MESG_FAILED_RETRIEVING =
            "Region '{}': Failed to retrieve function deployments - {}";
    private static final String MESG_PROCESSED_COUNT =
            "Region '{}': Processed '{}' function deployments";
    private static final String MESG_FAILED_PROCESS_DEPLOYMEMT =
            "Region '{}', Function version '{}': Failed to process deployment - '{}'";

    private static final String SPAN_FUNCTION_DEPLOYMENTS = "function-deployments";
    private static final String SPAN_HANDLE_DEPLOYMENT = "handle-function-deployment";
    private static final String SPAN_TAG_DEPLOYMENT_RECONCILIATION_REGION =
            "deployment_reconciliation_region";

    // Keep this executor open for reuse and use completable futures for individual task tracking.
    // The executor's threads were being interrupted on close, despite the javadoc saying it would
    // finish executing all submitted tasks first before shutting down.
    private final ExecutorService concurrentTaskExecutor;

    private final FunctionLookupService functionLookupService;
    private final GracefulDeploymentCleanupService gracefulDeploymentCleanupService;
    private final FunctionDeploymentReconciliationService functionDeploymentReconciliationService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final FunctionDeploymentsTaskProperties taskProperties;
    private final Tracer tracer;
    private final FunctionDeploymentService functionDeploymentService;

    public FunctionDeploymentsTask(
            FunctionLookupService functionLookupService,
            GracefulDeploymentCleanupService gracefulDeploymentCleanupService,
            FunctionDeploymentsTaskProperties taskProperties,
            FunctionDeploymentReconciliationService functionDeploymentReconciliationService,
            FunctionDeploymentLookupService functionDeploymentLookupService,
            Tracer tracer,
            FunctionDeploymentService functionDeploymentService) {
        this.functionLookupService = functionLookupService;
        this.gracefulDeploymentCleanupService = gracefulDeploymentCleanupService;
        this.concurrentTaskExecutor = Executors.newFixedThreadPool(
                taskProperties.getMaxConcurrency());
        this.functionDeploymentReconciliationService = functionDeploymentReconciliationService;
        this.functionDeploymentLookupService = functionDeploymentLookupService;
        this.taskProperties = taskProperties;
        this.tracer = tracer;
        this.functionDeploymentService = functionDeploymentService;
    }

    @Timed(value = "nvcf.scheduler.function.deployments")
    public void run() {
        var startTime = Instant.now();
        try {
            traceFunctionDeployments();
        } finally {
            var endTime = Instant.now();
            log.info(MESG_FUNCTION_DEPLOYMENTS_DURATION,
                     taskProperties.getCurrentRegion(),
                     startTime,
                     endTime,
                     Duration.between(startTime, endTime));
        }
    }

    @VisibleForTesting
    void traceFunctionDeployments() {
        NvcfUtils.inSpan(
                tracer,
                SPAN_FUNCTION_DEPLOYMENTS,
                Map.of(SPAN_TAG_DEPLOYMENT_RECONCILIATION_REGION, taskProperties.getCurrentRegion()),
                span -> {
                    processFunctionDeployments();
                    return null;
                });
    }

    private void processFunctionDeployments() {
        List<FunctionDeploymentEntity> ownDeployments;

        // In Phase 1, we will fetch all the deployments. Fetching all the deployments results
        // in approximately 450KB(for ~1800 deployments) to be retrieved which is much lower
        // than the 5MB limit. In Phase 2, we will only fetch region-specific deployments.
        try (var stream = functionDeploymentLookupService.lookupAllDeployments()) {
            // Materialize the region-owned deployments so the Cassandra-backed stream is consumed
            // and closed before asynchronous reconciliation begins. This avoids holding
            // Cassandra resources open for the duration of the scheduler/task run.
            ownDeployments = stream.filter(this::owns).toList();
        } catch (Exception ex) {
            log.warn(MESG_FAILED_RETRIEVING,
                     taskProperties.getCurrentRegion(), ex.getMessage(), ex);
            return;
        }

        // Populate the regional queue with all the deployments and maximize concurrency
        // instead of earlier serial account-level barrier scheme where most of the threads
        // were idle.
        var futures = ownDeployments.stream()
                .map(functionDeploymentLookupService::getDeploymentContext)
                .map(this::submitDeployment)
                .toArray(CompletableFuture[]::new);
        CompletableFuture.allOf(futures).join();
        log.info(MESG_PROCESSED_COUNT, taskProperties.getCurrentRegion(), ownDeployments.size());
    }

    private CompletableFuture<Void> submitDeployment(FunctionDeploymentContext deploymentContext) {
        return CompletableFuture.runAsync(() -> {
            try {
                handleFunctionDeployment(deploymentContext);
            } catch (Exception ex) {
                var versionId = deploymentContext.deployment().getKey().getFunctionVersionId();
                log.error(MESG_FAILED_PROCESS_DEPLOYMEMT,
                          taskProperties.getCurrentRegion(), versionId, ex.getMessage(), ex);
            }
        }, concurrentTaskExecutor);
    }

    @VisibleForTesting
    boolean owns(FunctionDeploymentEntity deployment) {
        var functionVersionId = deployment.getKey().getFunctionVersionId();
        var ownerIndex = Math.floorMod(functionVersionId.hashCode(),
                                       taskProperties.getRegions().size());
        return ownerIndex == taskProperties.getCurrentRegionIndex();
    }

    @Nullable
    private FunctionEntity handleFunctionDeployment(FunctionDeploymentContext deploymentContext) {
        var deployment = deploymentContext.deployment();
        return NvcfUtils.inSpan(
                tracer,
                SPAN_HANDLE_DEPLOYMENT,
                deploymentTags(deploymentContext),
                span -> functionLookupService.lookupUsingFunctionIdAndVersionId(
                                deployment.getFunctionId(),
                                deployment.getKey().getFunctionVersionId())
                        .map(function -> handleFunctionDeployment(
                                function,
                                deploymentContext,
                                span))
                        .orElse(null));
    }

    @Nullable
    @VisibleForTesting
    FunctionEntity handleFunctionDeployment(
            FunctionEntity function,
            FunctionDeploymentContext deploymentContext,
            Span span) {
        span.tag(SPAN_TAG_FUNCTION_STATUS, function.getFunctionStatus().toString());
        var deployment = deploymentContext.deployment();
        return switch (function.getFunctionStatus()) {
            case ACTIVE, DEPLOYING, DEGRADING, DEGRADED ->
                    // Instances are requested initially during deploy in FunctionDeploymentService.
                    // When the instances are active/running, the function will be set to active.
                    functionDeploymentReconciliationService.reconcile(
                            function, deploymentContext, taskProperties.getCurrentRegion());
            case ERROR ->
                    // Only clean deployments in ERROR status that have not been updated for 7 days.
                    functionDeploymentService.cleanupErroredDeployment(function, deployment);
            case INACTIVE ->
                    gracefulDeploymentCleanupService.cleanup(function, deploymentContext);
        };
    }

    private Map<String, Object> deploymentTags(FunctionDeploymentContext deploymentContext) {
        var functionId = deploymentContext.deployment().getFunctionId();
        var versionId = deploymentContext.deployment().getKey().getFunctionVersionId();
        var deploymentId = deploymentContext.deployment().getDeploymentId();
        var ncaId = deploymentContext.deployment().getNcaId();
        return Map.of(
                SPAN_TAG_FUNCTION_ID, functionId.toString(),
                SPAN_TAG_FUNCTION_VERSION_ID, versionId.toString(),
                SPAN_TAG_DEPLOYMENT_ID, deploymentId.toString(),
                SPAN_TAG_NCA_ID, ncaId,
                SPAN_TAG_DEPLOYMENT_RECONCILIATION_REGION, taskProperties.getCurrentRegion());
    }

    @Override
    public void close() {
        concurrentTaskExecutor.close();
    }
}
