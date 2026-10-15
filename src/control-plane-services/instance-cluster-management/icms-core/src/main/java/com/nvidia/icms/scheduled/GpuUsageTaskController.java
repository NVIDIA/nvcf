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
package com.nvidia.icms.scheduled;

import static com.nvidia.icms.configuration.SchedulingConfiguration.SCHEDULED_JOBS_PROFILES;
import static com.nvidia.icms.service.telemetry.TelemetryEventClient.POD_NAME_ENV_KEY;
import static com.nvidia.icms.util.InstanceServiceUtil.getAwsRegion;
import static com.nvidia.icms.util.InstanceServiceUtil.getPodName;

import com.google.common.base.Stopwatch;
import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.service.LockProviderService;
import com.nvidia.icms.service.scheduled.gpuusage.GpuUsageTask;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.TelemetryEventClient.EventMetaData;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.concurrent.ThreadLocalRandom;
import java.util.concurrent.TimeUnit;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Profile;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;
import java.security.SecureRandom;

@Slf4j
@Service
@AllArgsConstructor
@Profile(SCHEDULED_JOBS_PROFILES)
public class GpuUsageTaskController {

    public static final String GPU_USAGE_EVENT_NAME = "GpuUsageEvent";
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties icmsConfigurationProperties;
    private final TelemetryEventClient telemetryEventClient;
    private final GpuUsageTask gpuUsageTask;
    private static final SecureRandom SECURE_RANDOM = new SecureRandom();

    // Run at minute 0 of every hour
    @Scheduled(cron = "0 0 * * * *")
    public void sendGpuUsageEvent() {

        Stopwatch stopwatch = Stopwatch.createUnstarted();
        String capturedError = null;

        try {
            if (!icmsConfigurationProperties.isGpuUsagePerInstanceTaskEnabled()) {
                log.info("job: {} is not enabled", GPU_USAGE_EVENT_NAME);
                return;
            }

            sleepToAvoidContention();

            // lockTtl should be less than Cron duration as we want to make sure that lock is available when Cron runs
            if (!lockProviderService.obtainLockWithTtl(GPU_USAGE_EVENT_NAME,
                                                       icmsConfigurationProperties.getGpuUsageTaskLockTtlInSeconds())) {
                return;
            }
            stopwatch.start();
            gpuUsageTask.execute();

        } catch (Exception exception) {
            log.error("{} job failed with error: {} exception: ", GPU_USAGE_EVENT_NAME,
                      exception.getMessage(),
                      exception);

            capturedError = exception.getMessage();
        }

        // Sending event per job execution
        GenericMetric genericMetric = new GenericMetric()
                .withError(capturedError)
                .withMetadata(Map.of(TelemetryEventClient.EventMetaData.EXECUTION_TIME.getName(),
                                     stopwatch.elapsed(TimeUnit.SECONDS),
                                     EventMetaData.THREAD_NAME.getName(),
                                     Thread.currentThread().getName()))
                .withEventName(Events.GPU_USAGE_EVENT.toString());
        telemetryEventClient.triggerEvent(List.of(genericMetric));
    }

    private void sleepToAvoidContention()
            throws InterruptedException {
        try {
            int delayInSeconds = getRandomSleepDuration();

            Thread.sleep(delayInSeconds * 1000L);

        } catch (InterruptedException interruptedException) {

            // Setting interrupted flag
            Thread.currentThread().interrupt();

            log.error("job {}, {} current thread was interrupted with error {}",
                      GPU_USAGE_EVENT_NAME,
                      Thread.currentThread().getName(),
                      interruptedException.getMessage(),
                      interruptedException);

            throw interruptedException;
        }
    }

    /**
     * Controls random sleep generation mechanism based on feature flag
     *
     * @return a random sleep duration in seconds
     */
    int getRandomSleepDuration() {
        int bound = icmsConfigurationProperties.getGpuUsageTaskBoundSleepDurationInSeconds();
        if (icmsConfigurationProperties.isGpuUsageTaskSecureRandomEnabled()) {
            return getRandomSleepDurationUsingSecureRandom(bound);
        }

        return getRandomSleepDurationUsingPodNameHash(bound);
    }

    /**
     * Generates a random sleep duration using SecureRandom for unpredictability.
     *
     * <p>
     * Each call may return a different value, even for the same pod. The larger the bound, the lower the probability
     * that two pods will get the same value (i.e., a collision) in a single run.
     * For "almost certain" uniqueness (~4% chance of at least one collision), set bound to at least 10x the max number
     * of pods.
     *
     * <p>
     * If there is a collision due to the same random number for a job run, it might not occur for the next
     * job run.
     *
     * @param bound the upper bound (exclusive) for the sleep duration in seconds
     * @return a random sleep duration in seconds
     */
    private int getRandomSleepDurationUsingSecureRandom(int bound) {
        int sleepDuration = SECURE_RANDOM.nextInt(bound);
        log.info("job: {}, method: SECURE_RANDOM, making {} thread to sleep for {} seconds to avoid contention (SecureRandom)",
                GPU_USAGE_EVENT_NAME,
                Thread.currentThread().getName(),
                sleepDuration);
        return sleepDuration;
    }

    /**
     * Calculates a deterministic sleep duration for the pod to avoid lock contention.
     *
     * <p>
     * The method combines the pod name and region to create an entropy string, hashes it,
     * and uses the hash as a seed for a Random instance. The resulting value is in the range [0, bound).
     *
     * <p>
     * For a given pod (with a unique pod name and region), the delay will be constant for its lifetime.
     * This ensures that no two pods with different names/regions will get the same delay, as long as the
     * number of pods is less than the bound and pod names are unique. A pod with minimum delay will always
     * run the job
     *
     * <p>
     * If there is a collision due to the same random number for a job run, it will occur for the next
     * job run until next deployment happen and pod name changes.
     *
     * @param bound the upper bound (exclusive) for the sleep duration in seconds
     * @return a deterministic sleep duration in seconds for this pod
     */
    private int getRandomSleepDurationUsingPodNameHash(int bound) {
        String podName = getPodName();
        String region = getAwsRegion();

        // Combine podName and region for entropy
        String entropy = (podName != null ? podName : "")
                + (region != null ? region : "");

        int seed = entropy.hashCode();
        Random seededRandom = new Random(seed);

        int sleepDuration = seededRandom.nextInt(bound);
        log.info("job: {}, method: POD_NAME_HASH, pod: {}, region: {}, making {} thread to sleep for {} seconds to avoid contention",
                 GPU_USAGE_EVENT_NAME,
                 podName,
                 region,
                 Thread.currentThread().getName(),
                 sleepDuration);
        return sleepDuration;
    }
}
