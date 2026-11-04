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

import static com.nvidia.nvct.service.task.TaskPredicateUtils.taskAccessMatch;

import com.nvidia.nvct.rest.event.dto.ListEventsResponse;
import com.nvidia.nvct.service.event.EventService;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class EventFacade {

    private final EventService eventService;

    public ListEventsResponse getEvents(String ncaId,
                                        UUID taskId,
                                        int limit,
                                        String cursor,
                                        Authentication authentication) {
        var sliceContext = eventService.fetchEvents(ncaId, taskId, limit, cursor,
                taskEntity -> taskAccessMatch(authentication, Optional.of(taskId)));
        return new ListEventsResponse(sliceContext.events(),
                                      sliceContext.limit(),
                                      sliceContext.cursor());
    }
}
