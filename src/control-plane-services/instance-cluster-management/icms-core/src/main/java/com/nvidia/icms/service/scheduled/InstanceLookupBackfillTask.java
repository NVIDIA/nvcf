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
package com.nvidia.icms.service.scheduled;

import static com.nvidia.icms.configuration.SchedulingConfiguration.SCHEDULED_JOBS_PROFILES;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.inbound.rest.model.SpotInstanceInternalState;
import com.nvidia.icms.outbound.cassandra.instance.InstanceV2Repository;
import com.nvidia.icms.outbound.cassandra.instance.entity.InstanceV2Entity;
import com.nvidia.icms.outbound.cassandra.request.InstanceRequestV2Repository;
import com.nvidia.icms.service.LockProviderService;
import java.util.Objects;
import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Profile;
import org.springframework.scheduling.annotation.Scheduled;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@Profile(SCHEDULED_JOBS_PROFILES)
public class InstanceLookupBackfillTask {

    public static final String TASK_NAME = "InstanceLookupBackfillTask";

    private final InstanceV2Repository instanceRepository;
    private final InstanceRequestV2Repository requestRepository;
    private final LockProviderService lockProviderService;
    private final IcmsConfigurationProperties configuration;

    public InstanceLookupBackfillTask(
            InstanceV2Repository instanceRepository,
            InstanceRequestV2Repository requestRepository,
            LockProviderService lockProviderService,
            IcmsConfigurationProperties configuration) {
        this.instanceRepository = instanceRepository;
        this.requestRepository = requestRepository;
        this.lockProviderService = lockProviderService;
        this.configuration = configuration;
    }

    @Scheduled(
            initialDelayString = "${icms.instance-lookup-backfill-task-schedule-initial-delay:PT10S}",
            fixedDelayString = "${icms.instance-lookup-backfill-task-schedule-duration:PT1M}")
    public void execute() {
        var mode = configuration.getInstanceLookupBackfillTaskMode();
        if (mode == InstanceLookupBackfillMode.DISABLED) {
            return;
        }

        var lockName = TASK_NAME + "-" + mode;
        if (!lockProviderService.obtainLockWithTtl(
                lockName, configuration.getInstanceLookupBackfillTaskLockTtlInSeconds())) {
            return;
        }

        run(mode);
    }

    RunSummary run(InstanceLookupBackfillMode mode) {
        var summary = new MutableRunSummary(mode);
        log.info("Started ICMS instance lookup backfill: mode={}", mode);

        try {
            instanceRepository.findAllInstancesAndApplyAction(
                    instance -> processInstance(instance, mode, summary),
                    configuration.getInstanceLookupBackfillTaskPauseBetweenPagesInMs());
        } catch (Exception exception) {
            summary.failed++;
            log.error("ICMS instance lookup backfill scan failed: mode={}, error={}",
                    mode, exception.getMessage(), exception);
        }

        var result = summary.toRunSummary();
        log.info("Completed ICMS instance lookup backfill: mode={}, scanned={}, migrated={}, "
                        + "current={}, skippedTerminated={}, missingRequest={}, mismatched={}, "
                        + "failed={}",
                result.mode(), result.scanned(), result.migrated(), result.current(),
                result.skippedTerminated(), result.missingRequest(), result.mismatched(),
                result.failed());
        return result;
    }

    private void processInstance(
            InstanceV2Entity instance,
            InstanceLookupBackfillMode mode,
            MutableRunSummary summary) {
        summary.scanned++;
        try {
            var request = requestRepository.findRequestById(instance.getRequestId());
            if (request.isEmpty()) {
                if (instance.getInstanceStateName() == SpotInstanceInternalState.TERMINATED) {
                    summary.skippedTerminated++;
                    return;
                }
                summary.missingRequest++;
                log.warn("ICMS instance lookup backfill could not find request: "
                                + "instanceId={}, requestId={}",
                        instance.getInstanceId(), instance.getRequestId());
                return;
            }

            var expectedDeploymentId = request.get().getDeploymentId();
            var expectedGpuSpecificationId = request.get().getGpuSpecificationId();
            var changed = false;

            if (mode == InstanceLookupBackfillMode.MIGRATE) {
                if (instance.getDeploymentId() == null && expectedDeploymentId != null) {
                    instance.setDeploymentId(expectedDeploymentId);
                    changed = true;
                }
                if (instance.getGpuSpecificationId() == null
                        && expectedGpuSpecificationId != null) {
                    instance.setGpuSpecificationId(expectedGpuSpecificationId);
                    changed = true;
                }
                if (changed) {
                    instanceRepository.update(instance);
                    summary.migrated++;
                }
            }

            if (Objects.equals(instance.getDeploymentId(), expectedDeploymentId)
                    && Objects.equals(instance.getGpuSpecificationId(),
                            expectedGpuSpecificationId)) {
                if (!changed) {
                    summary.current++;
                }
            } else {
                summary.mismatched++;
                log.warn("ICMS instance lookup backfill found conflicting lookup values: "
                                + "instanceId={}, requestId={}",
                        instance.getInstanceId(), instance.getRequestId());
            }
        } catch (Exception exception) {
            summary.failed++;
            log.error("ICMS instance lookup backfill failed for instance: "
                            + "instanceId={}, requestId={}, mode={}, error={}",
                    instance.getInstanceId(), instance.getRequestId(), mode,
                    exception.getMessage(), exception);
        }
    }

    public record RunSummary(
            InstanceLookupBackfillMode mode,
            long scanned,
            long migrated,
            long current,
            long skippedTerminated,
            long missingRequest,
            long mismatched,
            long failed) {

        public boolean isValid() {
            return missingRequest == 0 && mismatched == 0 && failed == 0;
        }
    }

    private static class MutableRunSummary {
        private final InstanceLookupBackfillMode mode;
        private long scanned;
        private long migrated;
        private long current;
        private long skippedTerminated;
        private long missingRequest;
        private long mismatched;
        private long failed;

        private MutableRunSummary(InstanceLookupBackfillMode mode) {
            this.mode = mode;
        }

        private RunSummary toRunSummary() {
            return new RunSummary(mode, scanned, migrated, current, skippedTerminated,
                    missingRequest, mismatched, failed);
        }
    }
}
