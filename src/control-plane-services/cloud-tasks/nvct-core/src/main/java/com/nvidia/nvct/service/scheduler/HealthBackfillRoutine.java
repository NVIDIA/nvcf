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
package com.nvidia.nvct.service.scheduler;

import static com.nvidia.nvct.util.NvctConstants.BATCH_SIZE;

import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.task.TaskMapperService;
import java.util.Objects;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.data.domain.Pageable;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
@ConditionalOnProperty(name = "nvct.scheduled-routines.enabled",
        havingValue = "true",
        matchIfMissing = true)
public class HealthBackfillRoutine {

    static final String CONFIG_MODE =
            "nvct.scheduled-routines.health-backfill-routine.mode";

    private final TasksRepository tasksRepository;
    private final TaskMapperService taskMapperService;
    private final Mode mode;

    public HealthBackfillRoutine(
            TasksRepository tasksRepository,
            TaskMapperService taskMapperService,
            @Value("${" + CONFIG_MODE + ":DISABLED}") Mode mode) {
        this.tasksRepository = tasksRepository;
        this.taskMapperService = taskMapperService;
        this.mode = mode;
    }

    public RunSummary run() {
        if (mode == Mode.DISABLED) {
            return RunSummary.empty(mode);
        }

        var summary = new MutableRunSummary(mode);
        log.info("Started NVCT health backfill: mode={}", mode);

        try {
            var slice = tasksRepository.findAllBy(Pageable.ofSize(BATCH_SIZE).first());
            do {
                processSlice(slice, summary);
                if (!slice.hasNext()) {
                    break;
                }
                slice = tasksRepository.findAllBy(slice.nextPageable());
            } while (true);
        } catch (Exception exception) {
            summary.failed++;
            log.error("NVCT health backfill page failed: mode={}, error={}",
                    mode, exception.getMessage(), exception);
        }

        var result = summary.toRunSummary();
        log.info("Completed NVCT health backfill: mode={}, scanned={}, migrated={}, "
                        + "current={}, withoutLegacyHealth={}, missing={}, mismatched={}, failed={}",
                result.mode(), result.scanned(), result.migrated(), result.current(),
                result.withoutLegacyHealth(), result.missing(), result.mismatched(),
                result.failed());
        return result;
    }

    private void processSlice(Slice<TaskEntity> slice, MutableRunSummary summary) {
        slice.forEach(task -> processTask(task, summary));
    }

    private void processTask(TaskEntity task, MutableRunSummary summary) {
        summary.scanned++;
        try {
            var expected = TaskMapperService.toHealthDto(task.getLegacyHealthInfo());
            if (expected.isEmpty()) {
                summary.withoutLegacyHealth++;
                return;
            }

            if (StringUtils.isBlank(task.getHealth())) {
                if (mode == Mode.MIGRATE) {
                    var serialized = taskMapperService.serializeHealth(expected.get())
                            .orElseThrow(() -> new IllegalStateException(
                                    "Legacy health could not be serialized"));
                    task.setHealth(serialized);
                    tasksRepository.insert(task);
                    summary.migrated++;
                } else {
                    summary.missing++;
                }
                return;
            }

            var actual = taskMapperService.deserializeHealth(task.getHealth());
            if (actual.filter(value -> Objects.equals(value, expected.get())).isPresent()) {
                summary.current++;
            } else {
                summary.mismatched++;
                log.warn("NVCT health backfill found conflicting health: taskId={}",
                        task.getTaskId());
            }
        } catch (Exception exception) {
            summary.failed++;
            log.error("NVCT health backfill failed for task: taskId={}, mode={}, error={}",
                    task.getTaskId(), mode, exception.getMessage(), exception);
        }
    }

    public enum Mode {
        DISABLED,
        MIGRATE,
        VALIDATE
    }

    public record RunSummary(
            Mode mode,
            long scanned,
            long migrated,
            long current,
            long withoutLegacyHealth,
            long missing,
            long mismatched,
            long failed) {

        private static RunSummary empty(Mode mode) {
            return new RunSummary(mode, 0, 0, 0, 0, 0, 0, 0);
        }

        public boolean isValid() {
            return missing == 0 && mismatched == 0 && failed == 0;
        }
    }

    private static class MutableRunSummary {
        private final Mode mode;
        private long scanned;
        private long migrated;
        private long current;
        private long withoutLegacyHealth;
        private long missing;
        private long mismatched;
        private long failed;

        private MutableRunSummary(Mode mode) {
            this.mode = mode;
        }

        RunSummary toRunSummary() {
            return new RunSummary(mode, scanned, migrated, current, withoutLegacyHealth,
                    missing, mismatched, failed);
        }
    }
}
