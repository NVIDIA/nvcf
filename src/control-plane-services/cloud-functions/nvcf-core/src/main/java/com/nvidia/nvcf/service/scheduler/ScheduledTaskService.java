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

import static com.nvidia.nvcf.util.NvcfConstants.MAX_THREAD_POOL_SIZE;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_STATUS;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_FUNCTION_VERSION_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_INSTANCE_MANAGEMENT_TASK;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.FunctionDeploymentContext;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.function.FunctionDeploymentService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.core.annotation.Timed;
import io.micrometer.tracing.Span;
import io.micrometer.tracing.Tracer;
import io.nats.client.JetStreamApiException;
import jakarta.annotation.Nonnull;
import jakarta.annotation.Nullable;
import java.io.IOException;
import java.time.Duration;
import java.util.Map;
import java.util.Optional;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import lombok.extern.slf4j.Slf4j;
import net.javacrumbs.shedlock.core.LockAssert;
import net.javacrumbs.shedlock.spring.annotation.SchedulerLock;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.context.ApplicationListener;
import org.springframework.scheduling.annotation.EnableScheduling;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@ConditionalOnProperty(
        name = "nvcf.scheduled-tasks.enabled", havingValue = "true", matchIfMissing = true)
@EnableScheduling
public class ScheduledTaskService implements ApplicationListener<ApplicationReadyEvent>,
        AutoCloseable {

    private static final String CLEAN_NATS_STREAMS = "cleanNatsStreamsTask";
    private static final String FUNCTION_DEPLOYMENTS = "functionDeploymentsTask";
    private static final String MSG_BEGIN_TASK = "Begin task: '{}'";
    private static final String MSG_END_TASK = "End task: '{}'";
    private static final String SPAN_HANDLE_DEPLOYMENT = "handle-function-deployment";

    private final CountDownLatch initialised = new CountDownLatch(1);

    // Keep this executor open for reuse and use completable futures for individual task tracking.
    // The executor's threads were being interrupted on close, despite the javadoc saying it would
    // finish executing all submitted tasks first before shutting down.
    private final ExecutorService concurrentTaskExecutor;

    private final FunctionLookupService functionLookupService;
    private final GracefulCleanDeploymentTask gracefulCleanDeploymentTask;
    private final CleanNatsStreamsTask cleanNatsStreamsTask;
    private final InstanceManagementTask functionStatusManagementTask;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final AccountService accountService;
    private final Tracer tracer;
    private final FunctionDeploymentService functionDeploymentService;

    public ScheduledTaskService(
            FunctionLookupService functionLookupService,
            GracefulCleanDeploymentTask gracefulCleanDeploymentTask,
            CleanNatsStreamsTask cleanNatsStreamsTask,
            @Value("${nvcf.scheduled-tasks.executor-thread-count}")
            Optional<Integer> threadCount,
            InstanceManagementTask functionStatusManagementTask,
            FunctionDeploymentLookupService functionDeploymentLookupService,
            AccountService accountService,
            Tracer tracer,
            FunctionDeploymentService functionDeploymentService) {
        this.functionLookupService = functionLookupService;
        this.gracefulCleanDeploymentTask = gracefulCleanDeploymentTask;
        this.cleanNatsStreamsTask = cleanNatsStreamsTask;
        this.concurrentTaskExecutor = Executors.newFixedThreadPool(threadCount.orElseGet(
                () -> Math.min(MAX_THREAD_POOL_SIZE, Runtime.getRuntime().availableProcessors())));
        this.functionStatusManagementTask = functionStatusManagementTask;
        this.functionDeploymentLookupService = functionDeploymentLookupService;
        this.accountService = accountService;
        this.tracer = tracer;
        this.functionDeploymentService = functionDeploymentService;
    }

    @Override
    public void onApplicationEvent(@Nonnull ApplicationReadyEvent event) {
        initialised.countDown();
    }

    // There is currently one global leader.
    // TODO: split and share leadership responsibilities
    @Timed(value = "nvcf.scheduler.function.deployments")
    @SchedulerLock(name = FUNCTION_DEPLOYMENTS,
            lockAtLeastFor = "${nvcf.scheduled-tasks.deployment-tasks.fixed-delay:PT1M}",
            lockAtMostFor = "${nvcf.scheduled-tasks.deployment-tasks.fixed-delay:PT1M}")
    @Scheduled(fixedDelayString = "${nvcf.scheduled-tasks.deployment-tasks.fixed-delay:PT1M}")
    void functionDeployments()
            throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();

        var accountList = accountService.getAccounts();
        for (var account : accountList) {
            try (var stream = functionDeploymentLookupService
                    .getFunctionDeploymentContextByNcaId(account.getNcaId())) {
                // Stream from repository.findAllBy() is tied to Cassandra session. Cannot use
                // parallel streams as it results in multiple tasks accessing Cassandra's session
                // concurrently causing NPE.
                var futures = stream.map(deploymentContext -> CompletableFuture.runAsync(() -> {
                    try {
                        handleFunctionDeployment(deploymentContext);
                    } catch (Exception ex) {
                        log.error("Failed to process deployment for version {}",
                                  deploymentContext.deployment().getKey().getFunctionVersionId(),
                                  ex);
                    }
                }, concurrentTaskExecutor)).toArray(CompletableFuture[]::new);
                CompletableFuture.allOf(futures).join();
            } catch (Exception ex) {
                log.warn(ex.getMessage());
            }
        }
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
        return switch (function.getFunctionStatus()) {
            // only allocate instances for active functions.
            // initial function instances are requested during function deploy.
            // when the initial function instances come up, the function will
            // be set to active.
            case ACTIVE, DEPLOYING, DEGRADING, DEGRADED -> {
                yield functionStatusManagementTask.run(function, deploymentContext);
            }
            case ERROR -> {
                // Only clean deployments in ERROR status that have not been updated for 7 days.
                yield functionDeploymentService.cleanupErroredDeployment(
                        function, deploymentContext.deployment());
            }
            case INACTIVE -> {
                yield gracefulCleanDeploymentTask.run(function, deploymentContext);
            }
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
                SPAN_TAG_NCA_ID, ncaId);
    }

    // At any time, this periodic task should be run by just one node/instance in a
    // multi-region multi-instance deployment. A distributed lock is acquired by a
    // node/instance that will then run the task. Other nodes that failed to acquire
    // the lock, just give up and only attempt to acquire the lock when it has been
    // relinquished. When the task is completed, the lock is relinquished. A different
    // node/instance can acquire the distributed lock for running the task the next
    // time.
    //
    // By setting lockAtMostFor, we make sure that the lock is released even if the node
    // dies. By setting lockAtLeastFor, we make sure it's not executed more than once
    // during that time. Please note that lockAtMostFor is just a safety net in case
    // that the node executing the task dies, so set it to a time that is significantly
    // larger than maximum estimated task execution time. If the task takes longer than
    // lockAtMostFor, it may be executed again and the results will be unpredictable
    // (more processes will hold the lock).
    @SchedulerLock(name = CLEAN_NATS_STREAMS,
            lockAtLeastFor = "PT14M",
            lockAtMostFor = "PT14M")
    @Scheduled(fixedDelayString = "PT15M")
    void cleanNatsStreams()
            throws InterruptedException, JetStreamApiException, IOException {
        initialised.await();
        LockAssert.assertLocked();
        log.debug(MSG_BEGIN_TASK, CLEAN_NATS_STREAMS);
        cleanNatsStreamsTask.run(Duration.ofMinutes(15));
        log.debug(MSG_END_TASK, CLEAN_NATS_STREAMS);
    }

    @Override
    public void close() {
        concurrentTaskExecutor.close();
    }
}
