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
package com.nvidia.nvct.service.result;

import static com.datastax.oss.driver.api.core.data.ByteUtils.fromHexString;
import static com.datastax.oss.driver.api.core.data.ByteUtils.toHexString;
import static com.nvidia.nvct.util.NvctConstants.DEFAULT_PAGINATION_LIMIT;
import static com.nvidia.nvct.util.NvctConstants.MESG_INVALID_CURSOR;

import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ObjectNode;
import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.persistence.result.ResultsByTaskRepository;
import com.nvidia.nvct.persistence.result.entity.ResultByTaskEntity;
import com.nvidia.nvct.persistence.result.entity.ResultByTaskKey;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.result.dto.ResultDto;
import com.nvidia.nvct.service.task.TaskService;
import java.time.Instant;
import java.util.List;
import java.util.Objects;
import java.util.UUID;
import java.util.function.Predicate;
import lombok.Builder;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.data.cassandra.core.query.CassandraPageRequest;
import org.springframework.data.domain.PageRequest;
import org.springframework.data.domain.Slice;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ResultService {
    private static final String MESG_CANNOT_BE_NULL = "Parameter '%s' cannot be null";
    private static final String MESG_BLANK_PARAMETER = "'%s' cannot be blank";
    private static final String MESG_TASK_RESULT_OPERATION =
            "Task id '{}': {}";
    private static final String MESG_TASK_RESULT_IGNORED =
            "Task id '{}': Result ignored as resultHandlingStrategy is NONE";
    private static final String MESG_FORBIDDEN_TO_LIST_RESULTS =
            "Task '%s': Forbidden to list results";

    private final ResultsByTaskRepository resultsByTaskRepository;
    private final TaskService taskService;
    private final JsonMapper jsonMapper;

    @Builder
    public record ResultsSliceContext(List<ResultDto> results, String cursor, Integer limit) { }

    public ResultsSliceContext fetchResults(String ncaId, UUID taskId, int limit, String cursor,
                                            Predicate<TaskEntity> taskAccessMatch) {
        log.debug(MESG_TASK_RESULT_OPERATION, taskId, "Fetching results");
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        if (!taskAccessMatch.test(taskEntity)) {
            var mesg = MESG_FORBIDDEN_TO_LIST_RESULTS.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        Slice<ResultByTaskEntity> pagedResult;
        try {
            var byteBuffer = cursor == null ? null : fromHexString(cursor);
            var pageable = PageRequest.of(0, limit);
            var pageRequest = CassandraPageRequest.of(pageable, byteBuffer);
            pagedResult = resultsByTaskRepository.findByKeyTaskId(taskId, pageRequest);
        } catch (Exception e) {
            var mesg = MESG_INVALID_CURSOR.formatted(cursor);
            log.error(mesg);
            throw new BadRequestException(mesg, e);
        }

        var dtos = pagedResult.getContent().stream()
                .map(this::toResultDto)
                .toList();
        var builder = ResultsSliceContext.builder().results(dtos);
        if (pagedResult.hasNext()) {
            var pagingState = ((CassandraPageRequest) pagedResult.getPageable()).getPagingState();
            builder.cursor(toHexString(pagingState));
            builder.limit(limit);
        }

        log.debug(MESG_TASK_RESULT_OPERATION, taskId, "Fetched results");
        return builder.build();
    }

    @VisibleForTesting
    public List<ResultDto> fetchResults(String ncaId, UUID taskId) {
        var results = fetchResults(ncaId, taskId, Integer.parseInt(DEFAULT_PAGINATION_LIMIT), null,
                taskEntity -> true); 
        return results.results();
    }

    public boolean deleteResults(String ncaId, UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // If the Task was deleted but for some reason results were not deleted, then don't
        // throw an exception and continue deleting results.
        taskService.lookupTask(taskId).ifPresent(task -> taskService.validateAccount(ncaId, task));
        resultsByTaskRepository.deleteByKeyTaskId(taskId);
        log.info(MESG_TASK_RESULT_OPERATION, taskId, "Deleted results");
        return true;
    }

    public ResultByTaskEntity insertResult(
            TaskEntity taskEntity,
            String name,
            String metadata) {
        var ncaId = taskEntity.getNcaId();
        var taskId = taskEntity.getTaskId();
        taskService.validateAccount(ncaId, taskId);
        if (taskEntity.getResultHandlingStrategy() == ResultHandlingStrategy.NONE) {
            // If resultHandlingStrategy is NONE, we will ignore calls to insert an entry
            // in the results_by_task table. This can happen when the Task Container
            // updates progress file with just percentComplete set to 100 to indicate
            // a graceful exit so that Utils Container can mark the Task as COMPLETED.
            log.debug(MESG_TASK_RESULT_IGNORED, taskId);
            return null;
        }

        if (StringUtils.isBlank(name)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("name");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        if (StringUtils.isBlank(metadata)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("metadata");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        var resultEntity = ResultByTaskEntity.builder()
                .key(ResultByTaskKey.builder().taskId(taskId).resultId(UUID.randomUUID()).build())
                .createdAt(Instant.now())
                .ncaId(ncaId)
                .name(name)
                .metadata(metadata)
                .build();
        resultEntity = resultsByTaskRepository.save(resultEntity);
        log.info(MESG_TASK_RESULT_OPERATION, taskId, "Inserted result");
        return resultEntity;
    }

    // Used by cleanup subroutine that executes periodically. There is no validation performed
    // to match the NCA Id as there could be scenario where the Task entry is deleted but the
    // results were not deleted. If the validation kicks in, then it will result in
    // NotFoundException and we end up with partially cleaned Task.
    public void cleanResults(UUID taskId) {
        Objects.requireNonNull(taskId, () -> MESG_CANNOT_BE_NULL.formatted("taskId"));
        resultsByTaskRepository.deleteByKeyTaskId(taskId);
    }

    @SneakyThrows
    private ResultDto toResultDto(ResultByTaskEntity entity) {
        return ResultDto.builder()
                .resultId(entity.getKey().getResultId())
                .taskId(entity.getKey().getTaskId())
                .ncaId(entity.getNcaId())
                .name(entity.getName())
                .metadata(jsonMapper.readValue(entity.getMetadata(), ObjectNode.class))
                .createdAt(entity.getCreatedAt())
                .build();
    }

}
