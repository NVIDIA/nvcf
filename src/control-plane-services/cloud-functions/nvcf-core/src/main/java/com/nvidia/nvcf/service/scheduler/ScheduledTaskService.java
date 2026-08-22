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

import io.nats.client.JetStreamApiException;
import jakarta.annotation.Nonnull;
import java.io.IOException;
import java.time.Duration;
import java.util.concurrent.CountDownLatch;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import net.javacrumbs.shedlock.core.LockAssert;
import net.javacrumbs.shedlock.spring.annotation.SchedulerLock;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.context.event.ApplicationReadyEvent;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.ApplicationListener;
import org.springframework.scheduling.annotation.EnableScheduling;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@ConditionalOnProperty(
        name = "nvcf.scheduler.enabled", havingValue = "true", matchIfMissing = true)
@EnableScheduling
@RequiredArgsConstructor
@RefreshScope
public class ScheduledTaskService implements ApplicationListener<ApplicationReadyEvent> {

    private static final String CLEAN_NATS_STREAMS = "cleanNatsStreamsTask";
    private static final String FUNCTION_DEPLOYMENTS = "functionDeploymentsTask";

    private static final String MESG_BEGIN_TASK = "Begin task: '{}'";
    private static final String MESG_END_TASK = "End task: '{}'";
    private final CountDownLatch initialised = new CountDownLatch(1);

    private final FunctionDeploymentsTask functionDeploymentsTask;
    private final CleanNatsStreamsTask cleanNatsStreamsTask;
    private final FunctionDeploymentsRegionProvider regionProvider;

    @Override
    public void onApplicationEvent(@Nonnull ApplicationReadyEvent event) {
        initialised.countDown();
    }

    // One leader in each region processes the deployments assigned to that region. Use
    // EL expression to create region-specific lock names.
    @SchedulerLock(name = FUNCTION_DEPLOYMENTS
            + "-#{@functionDeploymentsRegionProvider.currentRegion()}",
            lockAtLeastFor = "${nvcf.scheduler.function-deployments.lock-at-least-for:PT2M}",
            lockAtMostFor = "${nvcf.scheduler.function-deployments.lock-at-most-for:PT4M}")
    @Scheduled(fixedDelayString = "${nvcf.scheduler.function-deployments.fixed-delay:PT1M}")
    void functionDeployments()
            throws InterruptedException {
        initialised.await();
        LockAssert.assertLocked();
        var regionalName = FUNCTION_DEPLOYMENTS + "-" + regionProvider.currentRegion();
        log.debug(MESG_BEGIN_TASK, regionalName);
        functionDeploymentsTask.run();
        log.debug(MESG_END_TASK, FUNCTION_DEPLOYMENTS);
    }

    // Generic comment applies to all methods using SchedulerLock annotation to get
    // a distributed lock.
    //
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
        log.debug(MESG_BEGIN_TASK, CLEAN_NATS_STREAMS);
        cleanNatsStreamsTask.run(Duration.ofMinutes(15));
        log.debug(MESG_END_TASK, CLEAN_NATS_STREAMS);
    }
}
