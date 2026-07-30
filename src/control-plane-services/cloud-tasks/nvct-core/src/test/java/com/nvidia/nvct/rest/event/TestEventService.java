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
package com.nvidia.nvct.rest.event;

import com.nvidia.nvct.persistence.event.EventsByTaskRepository;
import com.nvidia.nvct.persistence.event.entity.EventByTaskEntity;
import com.nvidia.nvct.persistence.event.entity.EventByTaskKey;
import java.time.Instant;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TestEventService {

    private final EventsByTaskRepository eventsByTaskRepository;

    public void populateEventForTask(UUID eventId,
                                     String ncaId,
                                     UUID taskId,
                                     String message) {
        var event = EventByTaskEntity.builder()
                .key(EventByTaskKey.builder().taskId(taskId).eventId(eventId).build())
                .ncaId(ncaId)
                .message(message)
                .createdAt(Instant.now())
                .build();
        eventsByTaskRepository.save(event);
    }

}
