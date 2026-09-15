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
package com.nvidia.nvct.rest.task;

import static com.nvidia.nvct.util.NvctConstants.DEFAULT_PAGINATION_LIMIT;
import static com.nvidia.nvct.util.NvctConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvct.rest.task.dto.BulkTaskDetailsRequest;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.ListBasicTaskDetailsResponse;
import com.nvidia.nvct.rest.task.dto.ListTasksResponse;
import com.nvidia.nvct.rest.task.dto.TaskResponse;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.ratelimit.RateLimiterService;
import com.nvidia.nvct.util.NvctUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Positive;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.ResponseEntity;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Tag(name = "Task Management",
        description = """
                Defines Task Management endpoints. All tne endpoints defined in this API require
                 a bearer token with appropriate scope in the HTTP Authorization header.
                 """
)
public class TaskManagementController {

    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final TaskManagementFacade taskManagementFacade;
    private final Tracer tracer;

    @PostMapping(value = "/v1/nvct/tasks", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Create and Launch Task",
            description = """
                    Creates and launches a new task within the authenticated NVIDIA Cloud Account.
                     Requires a bearer token with 'launch_task' scope in the HTTP Authorization
                     header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('launch_task', 'apikey:launch_task')")
    public TaskResponse createAndLaunchTask(
            @Valid @NotNull @RequestBody CreateTaskRequest createRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return taskManagementFacade.createAndLaunchTask(ncaId, createRequest,
                httpServletRequest, authentication);
    }

    @PostMapping(value = "/v1/nvct/tasks/bulk", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "List Basic Details of Specific Tasks",
            description = """
                    Lists basic details such as status, etc. of specified Task Ids. All the
                     Tasks should belong to the authenticated NVIDIA Cloud Account. Requires
                     a bearer token 'list_tasks' scopes in the HTTP Authorization header."""
    )
    @PreAuthorize("hasAnyAuthority('list_tasks', 'apikey:list_tasks')")
    public ListBasicTaskDetailsResponse listBasicDetailsOfSpecificTasks(
            @Valid @NotNull @RequestBody BulkTaskDetailsRequest listBasicTasksRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return taskManagementFacade
                .listBasicDetailsOfSpecificTasks(ncaId, listBasicTasksRequest, authentication);
    }


    @GetMapping("/v1/nvct/tasks")
    @Operation(
            summary = "List Tasks",
            description = """
                    Lists all the tasks associated with the authenticated NVIDIA Cloud Account.
                     Requires a bearer token with 'list_tasks' scopes in the HTTP Authorization
                     header."""
    )
    @PreAuthorize("hasAnyAuthority('list_tasks', 'apikey:list_tasks')")
    public ListTasksResponse listTasks(
            @Parameter(description = "Number of tasks to return in the response")
            @RequestParam(required = false, defaultValue = DEFAULT_PAGINATION_LIMIT)
            @Positive int limit,
            @Parameter(description = "Task status")
            @RequestParam(required = false) TaskStatusEnum status,
            @Parameter(description = "Beginning of the pagination slice")
            @RequestParam(required = false) String cursor,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        return taskManagementFacade.listTasks(ncaId, authentication, limit, status, cursor);
    }

    @GetMapping("/v1/nvct/tasks/{taskId}")
    @Operation(
            summary = "Get Task Details",
            description = """
                    Gets details of specified task in the authenticated NVIDIA Cloud Account.
                     Requires a bearer token with 'task_details' scope in the HTTP Authorization
                     header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('task_details', 'apikey:task_details')")
    public TaskResponse getTaskDetails(
            @Parameter(description = "Task id", required = true)
            @PathVariable("taskId") UUID taskId,
            @Parameter(description = "Indicates whether to include secret names in the response")
            @RequestParam(required = false, defaultValue = "true") boolean includeSecrets,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, taskId);
        return taskManagementFacade.getTaskDetails(ncaId, taskId, authentication, includeSecrets);
    }

    @PostMapping("/v1/nvct/tasks/{taskId}/cancel")
    @Operation(
            summary = "Cancel Task",
            description = """
                    Cancels the specified task in the authenticated NVIDIA Cloud Account. Requires
                     a bearer token with 'cancel_task' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('cancel_task', 'apikey:cancel_task')")
    public TaskResponse cancelTask(
            @Parameter(description = "Task id", required = true)
            @PathVariable("taskId") UUID taskId,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, taskId);
        return taskManagementFacade.cancelTask(ncaId, taskId,
                httpServletRequest, authentication);
    }

    @DeleteMapping("/v1/nvct/tasks/{taskId}")
    @Operation(
            summary = "Delete Task",
            description = """
                    Deletes the specified task in the authenticated NVIDIA Cloud Account. Requires
                     a bearer token with 'delete_task' scope in the HTTP Authorization header.
                    """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('delete_task', 'apikey:delete_task')")
    public ResponseEntity<Void> deleteTask(
            @Parameter(description = "Task id", required = true)
            @PathVariable("taskId") UUID taskId,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvctUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId, taskId);
        return taskManagementFacade.deleteTask(ncaId, taskId,
                httpServletRequest, authentication);
    }

}
