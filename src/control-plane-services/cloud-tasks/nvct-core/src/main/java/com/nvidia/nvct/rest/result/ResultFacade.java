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
package com.nvidia.nvct.rest.result;

import static com.nvidia.nvct.service.task.TaskPredicateUtils.taskAccessMatch;

import com.nvidia.nvct.rest.result.dto.ListResultsResponse;
import com.nvidia.nvct.service.result.ResultService;
import java.util.Optional;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class ResultFacade {

    private final ResultService resultService;

    public ListResultsResponse getResults(String ncaId,
                                          UUID taskId,
                                          int limit,
                                          String cursor,
                                          Authentication authentication) {
        var sliceContext = resultService.fetchResults(ncaId, taskId, limit, cursor,
                taskEntity -> taskAccessMatch(authentication, Optional.of(taskEntity.getTaskId())));
        return new ListResultsResponse(sliceContext.results(),
                                       sliceContext.limit(),
                                       sliceContext.cursor());
    }

}
