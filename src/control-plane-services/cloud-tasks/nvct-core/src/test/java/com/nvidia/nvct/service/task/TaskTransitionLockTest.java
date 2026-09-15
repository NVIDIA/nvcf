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
package com.nvidia.nvct.service.task;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.util.UUID;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import org.junit.jupiter.api.Test;

class TaskTransitionLockTest {
    private final TaskTransitionLock taskTransitionLock = new TaskTransitionLock();

    @Test
    void serializesTransitionsForTheSameTask() throws Exception {
        var taskId = UUID.randomUUID();
        try (var executor = Executors.newFixedThreadPool(2);
             var ignored = taskTransitionLock.acquire(taskId)) {
            var blockedTransition = executor.submit(() -> {
                try (var acquired = taskTransitionLock.acquire(taskId)) {
                    return true;
                }
            });

            assertThatThrownBy(() -> blockedTransition.get(100, TimeUnit.MILLISECONDS))
                    .isInstanceOf(TimeoutException.class);
            assertThat(blockedTransition.isDone()).isFalse();
        }
    }

    @Test
    void permitsTransitionsForDifferentTasks() throws Exception {
        var taskId = UUID.randomUUID();
        try (var executor = Executors.newSingleThreadExecutor();
             var ignored = taskTransitionLock.acquire(taskId)) {
            var otherTransition = executor.submit(() -> {
                try (var acquired = taskTransitionLock.acquire(UUID.randomUUID())) {
                    return true;
                }
            });

            assertThat(otherTransition.get(1, TimeUnit.SECONDS)).isTrue();
        }
    }
}
