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
package com.nvidia.nvcf.rest.queue;

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.authorizedFunctionMatch;
import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_INVOCATION_REQUEST_ID;
import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static java.lang.String.format;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.rest.function.invocation.FunctionInvocationFacade;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionStatusEnum;
import com.nvidia.nvcf.rest.queue.dto.GetPositionInQueueResponse;
import com.nvidia.nvcf.rest.queue.dto.GetQueuesResponse;
import com.nvidia.nvcf.rest.queue.dto.QueueDto;
import com.nvidia.nvcf.service.function.FunctionManagementService;
import com.nvidia.nvcf.service.resultregion.ResultRegistrationService;
import com.nvidia.nvcf.service.worker.WorkerNatsService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import jakarta.annotation.Nullable;
import java.util.EnumSet;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class QueueFacade {

    private static final String MESG_FUNCTION_QUEUE_NOT_FOUND =
            "Function id '%s': Queue not found - Function is likely not deployed";
    private static final String MESG_REQUEST_ID_NOT_IN_PENDING_OR_IN_PROGRESS_STATE =
            "Function invocation request id '%s': Invocation request message is not in pending "
                    + "or in progress state";
    private static final EnumSet<FunctionStatusEnum> ACTIVE_OR_DEPLOYING =
            EnumSet.of(FunctionStatusEnum.ACTIVE, FunctionStatusEnum.DEPLOYING);

    private final FunctionManagementService functionManagementService;
    private final WorkerNatsService workerNatsService;
    private final FunctionInvocationFacade functionInvocationFacade;
    private final ResultRegistrationService resultRegistrationService;
    private final Tracer tracer;

    public GetQueuesResponse getQueuesDetails(
            String ncaId,
            Authentication authentication,
            UUID functionId,
            @Nullable UUID functionVersionId) {
        List<FunctionDto> candidateFunctions;

        // If functionVersionId is specified, then pick an exact function.
        if (functionVersionId != null) {
            candidateFunctions =
                    List.of(functionManagementService
                                    .getFunction(ncaId,
                                                 functionId,
                                                 functionVersionId,
                                                 function -> privateFunctionMatch(ncaId,
                                                                                  authentication,
                                                                                  function),
                                                 function -> authorizedFunctionMatch(ncaId,
                                                                                     authentication,
                                                                                     function),
                                                 false, false));
        } else {
            candidateFunctions = functionManagementService
                    .getFunctions(ncaId,
                                  functionId,
                                  function -> privateFunctionMatch(ncaId, authentication, function),
                                  function -> authorizedFunctionMatch(ncaId,
                                                                      authentication,
                                                                      function));
        }

        var queueDtos = candidateFunctions
                .stream()
                .filter(function -> ACTIVE_OR_DEPLOYING.contains(function.status()))
                .map(this::fetchQueueDepth)
                .toList();
        if (CollectionUtils.isEmpty(queueDtos)) {
            var mesg = MESG_FUNCTION_QUEUE_NOT_FOUND.formatted(functionId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
        return GetQueuesResponse.builder()
                .functionId(functionId).queues(queueDtos).build();
    }

    private QueueDto fetchQueueDepth(FunctionDto function) {
        var depth = workerNatsService.queueDepth(function.versionId());
        return toQueueDto(function, depth.intValue());
    }

    private static QueueDto toQueueDto(FunctionDto function, int depth) {
        var status = function.status();
        var name = function.name();
        var versionId = function.versionId();
        return QueueDto.builder()
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(status)
                .queueDepth(depth)
                .build();
    }

    public GetPositionInQueueResponse getPositionInQueue(
            String ncaId,
            UUID requestId,
            Authentication authentication) {
        tracePositionRequest(ncaId, requestId);
        return getPositionInQueueResponseNats(requestId, ncaId, authentication);
    }

    private void tracePositionRequest(
            String ncaId, UUID requestId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(
                SPAN_TAG_NCA_ID, ncaId,
                SPAN_TAG_INVOCATION_REQUEST_ID, requestId.toString()));
    }

    private GetPositionInQueueResponse getPositionInQueueResponseNats(
            UUID requestId, String ncaId, Authentication authentication) {
        var workerResultTracking = resultRegistrationService.findRequest(requestId);
        if (workerResultTracking == null) {
            var mesg = MESG_REQUEST_ID_NOT_IN_PENDING_OR_IN_PROGRESS_STATE.formatted(requestId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        var functionId = UUID.fromString(workerResultTracking.getFunctionId());
        var functionVersionId = UUID.fromString(workerResultTracking.getFunctionVersionId());

        functionInvocationFacade.validateAccess(functionId, functionVersionId, requestId,
                                                ncaId, authentication);
        var pos = workerNatsService.positionInQueue(functionVersionId, requestId);
        if (pos.intValue() == -1) {
            var mesg = format(MESG_REQUEST_ID_NOT_IN_PENDING_OR_IN_PROGRESS_STATE, requestId);
            log.error(mesg, requestId);
            throw new BadRequestException(mesg);
        }

        return GetPositionInQueueResponse.builder()
                .functionVersionId(functionVersionId)
                .functionId(functionId)
                .positionInQueue(pos.intValue()).build();
    }
}
