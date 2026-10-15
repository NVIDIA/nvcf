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
package com.nvidia.nvct.service.event;

import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;
import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;
import static com.nvidia.nvct.util.NvctConstants.DEFAULT_PAGINATION_LIMIT;
import static com.nvidia.nvct.util.NvctConstants.MESG_INVALID_CURSOR;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.persistence.event.EventsByTaskRepository;
import com.nvidia.nvct.persistence.event.entity.EventByTaskEntity;
import com.nvidia.nvct.persistence.event.entity.EventByTaskKey;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.event.dto.EventDto;
import com.nvidia.nvct.service.task.TaskService;
import java.time.Instant;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Predicate;
import java.util.regex.Pattern;
import lombok.Builder;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class EventService {
    public static final String STATUS_CHANGE_EVENT_MESSAGE =
            "Changing status from '%s' to '%s'";
    public static final String STATUS_CHANGE_EVENT_MESSAGE_WITH_ERROR =
            "Changing status from '%s' to '%s' with error '%s'";

    // Pattern to match status change event messages such as above.
    private static final Pattern STATUS_CHANGE_PATTERN =
            Pattern.compile("Changing status from '[^']*' to '([^']+)'");

    private static final String MESG_CANNOT_BE_NULL = "Parameter '%s' cannot be null";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";

    private static final String MESG_TASK_EVENT_OPERATION =
            "Task id '{}': {}";
    private static final String MESG_FAILED_TO_PARSE_EVENT_MESSAGE =
            "Task id '{}': Could not parse status '{}' from event message: {}";
    private static final String MESG_ERROR_RETRIEVING_TERMINAL_STATUS_FROM_EVENT =
            "Task id '{}': Error retrieving terminal status from events: {}";
    private static final String MESG_FORBIDDEN_TO_LIST_EVENTS = 
            "Task '%s': Forbidden to list events";

    private final EventsByTaskRepository eventsByTaskRepository;
    private final TaskService taskService;

    @Builder
    public record EventsSliceContext(List<EventDto> events, String cursor, Integer limit) { }

    public EventsSliceContext fetchEvents(String ncaId, UUID taskId, int limit, String cursor,
                                          Predicate<TaskEntity> taskAccessMatch) {
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        if (!taskAccessMatch.test(taskEntity)) {
            var mesg = MESG_FORBIDDEN_TO_LIST_EVENTS.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        Slice<EventByTaskEntity> pagedResult;
        try {
            var byteBuffer = cursor == null ? null : fromHexString(cursor);
            var pageable = PageRequest.of(0, limit);
            var pageRequest = CassandraPageRequest.of(pageable, byteBuffer);
            pagedResult = eventsByTaskRepository.findByKeyTaskId(taskId, pageRequest);
        } catch (Exception e) {
            var mesg = MESG_INVALID_CURSOR.formatted(cursor);
            log.error(mesg);
            throw new BadRequestException(mesg, e);
        }
        var dtos = pagedResult.getContent().stream()
                .map(EventService::toEventDto)
                .toList();
        var builder = EventsSliceContext.builder().events(dtos);
        if (pagedResult.hasNext()) {
            var pagingState = ((CassandraPageRequest) pagedResult.getPageable()).getPagingState();
            builder.cursor(toHexString(pagingState));
            builder.limit(limit);
        }
        log.debug(MESG_TASK_EVENT_OPERATION, taskId, "Fetched events");
        return builder.build();
    }

    @VisibleForTesting
    public List<EventDto> fetchEvents(String ncaId, UUID taskId) {
        var events = fetchEvents(ncaId, taskId, Integer.parseInt(DEFAULT_PAGINATION_LIMIT), null,
                taskEntity -> true);
        return events.events();
    }

    public boolean deleteEvents(String ncaId, UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // If the Task was deleted but for some reason events were not deleted, then don't
        // throw an exception and continue deleting events.
        taskService.lookupTask(taskId).ifPresent(task -> taskService.validateAccount(ncaId, task));
        eventsByTaskRepository.deleteByKeyTaskId(taskId);
        log.info(MESG_TASK_EVENT_OPERATION, taskId, "Deleted task events");
        return true;
    }

    public EventByTaskEntity insertEvent(String ncaId, UUID taskId, String message) {
        taskService.validateAccount(ncaId, taskId);
        if (StringUtils.isBlank(message)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("message");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var eventEntity = EventByTaskEntity.builder()
                .key(EventByTaskKey.builder().taskId(taskId).eventId(UUID.randomUUID()).build())
                .createdAt(Instant.now())
                .ncaId(ncaId)
                .message(message)
                .build();
        eventEntity = eventsByTaskRepository.save(eventEntity);
        log.info(MESG_TASK_EVENT_OPERATION, taskId, "Inserted event");
        return eventEntity;
    }

    // Used by cleanup subroutine that executes periodically. There is no validation performed
    // to match the NCA Id as there could be scenario where the Task entry is deleted but the
    // events were not deleted. If the validation kicks in, then it will result in
    // NotFoundException and we end up with partially cleaned Task.
    public void cleanEvents(UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        eventsByTaskRepository.deleteByKeyTaskId(taskId);
    }

    // Retrieves task events and checks if the latest event contains a terminal status transition.
    // Returns the terminal status if found, otherwise returns empty.
    public Optional<TaskStatus> getTerminalStatusFromLatestEvent(UUID taskId) {
        try {
            // Fetch all events for this task and get the latest one by creation time.
            var latestEventOpt = eventsByTaskRepository
                    .findByKeyTaskId(taskId)
                    .max(Comparator.comparing(EventByTaskEntity::getCreatedAt));

            return latestEventOpt.flatMap(EventService::parseEventMessage);
        } catch (Exception e) {
            log.error(MESG_ERROR_RETRIEVING_TERMINAL_STATUS_FROM_EVENT, taskId, e.getMessage(), e);
            return Optional.empty();
        }
    }

    // ### TODO: Parse the event message to extract the target status. This is temporary.
    //           We should enhance the events_by_task table to have a separate field for the
    //           new/target status.
    private static Optional<TaskStatus> parseEventMessage(EventByTaskEntity taskEvent) {
        var message = taskEvent.getMessage();
        var taskId = taskEvent.getKey().getTaskId();

        var matcher = STATUS_CHANGE_PATTERN.matcher(message);
        if (matcher.find()) {
            var rawStatus = matcher.group(1);
            try {
                var status = TaskStatus.fromText(rawStatus);
                if (TERMINAL_TASK_STATUSES.contains(status)) { // Check if terminal status
                    return Optional.of(status);
                }
            } catch (IllegalStateException e) {
                log.warn(MESG_FAILED_TO_PARSE_EVENT_MESSAGE, taskId, rawStatus, e.getMessage());
            }
        }

        return Optional.empty();
    }

    private static EventDto toEventDto(EventByTaskEntity entity) {
        return EventDto.builder()
                .eventId(entity.getKey().getEventId())
                .taskId(entity.getKey().getTaskId())
                .ncaId(entity.getNcaId())
                .message(entity.getMessage())
                .createdAt(entity.getCreatedAt())
                .build();
    }
}
