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

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvct.persistence.task.TasksRepository;
import com.nvidia.nvct.persistence.task.entity.HealthUdt;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.service.task.TaskMapperService;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.data.domain.SliceImpl;

@ExtendWith(MockitoExtension.class)
class HealthBackfillRoutineTest {

    @Mock
    private TasksRepository tasksRepository;

    @Mock
    private TaskMapperService taskMapperService;

    private TaskEntity task;
    private HealthDto expectedHealth;

    @BeforeEach
    void setUp() {
        task = mock(TaskEntity.class);
        var taskId = UUID.randomUUID();
        var legacyHealth = HealthUdt.builder()
                .legacyIcmsRequestId(UUID.randomUUID())
                .gpu("A100")
                .backend("test")
                .instanceType("test.large")
                .error("test error")
                .build();
        expectedHealth = HealthDto.builder()
                .gpu("A100")
                .backend("test")
                .instanceType("test.large")
                .error("test error")
                .build();
        lenient().when(task.getTaskId()).thenReturn(taskId);
        lenient().when(task.getLegacyHealthInfo()).thenReturn(legacyHealth);
        lenient().when(tasksRepository.findAllBy(any()))
                .thenReturn(new SliceImpl<>(List.of(task)));
    }

    @Test
    void migrateCopiesMissingLegacyHealth() {
        when(taskMapperService.serializeHealth(expectedHealth))
                .thenReturn(Optional.of("{\"gpu\":\"A100\"}"));

        var summary = routine(HealthBackfillRoutine.Mode.MIGRATE).run();

        verify(task).setHealth("{\"gpu\":\"A100\"}");
        verify(tasksRepository).insert(task);
        assertThat(summary.migrated()).isEqualTo(1);
        assertThat(summary.isValid()).isTrue();
    }

    @Test
    void validateReportsMissingHealthWithoutWriting() {
        var summary = routine(HealthBackfillRoutine.Mode.VALIDATE).run();

        verify(tasksRepository, never()).insert(any(TaskEntity.class));
        assertThat(summary.missing()).isEqualTo(1);
        assertThat(summary.isValid()).isFalse();
    }

    @Test
    void validateReportsConflictingHealthWithoutOverwriting() {
        when(task.getHealth()).thenReturn("{\"gpu\":\"H100\"}");
        when(taskMapperService.deserializeHealth(task.getHealth()))
                .thenReturn(Optional.of(HealthDto.builder()
                        .gpu("H100")
                        .backend("test")
                        .instanceType("test.large")
                        .error("test error")
                        .build()));

        var summary = routine(HealthBackfillRoutine.Mode.VALIDATE).run();

        verify(tasksRepository, never()).insert(any(TaskEntity.class));
        assertThat(summary.mismatched()).isEqualTo(1);
        assertThat(summary.isValid()).isFalse();
    }

    @Test
    void disabledDoesNotReadTasks() {
        var summary = routine(HealthBackfillRoutine.Mode.DISABLED).run();

        verify(tasksRepository, never()).findAllBy(any());
        assertThat(summary.scanned()).isZero();
    }

    private HealthBackfillRoutine routine(HealthBackfillRoutine.Mode mode) {
        return new HealthBackfillRoutine(tasksRepository, taskMapperService, mode);
    }
}
