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

import static com.nvidia.nvct.service.event.EventService.STATUS_CHANGE_EVENT_MESSAGE;
import static com.nvidia.nvct.service.secret.SecretManagementService.getNgcApiKey;
import static com.nvidia.nvct.service.secret.SecretManagementService.hasDupeSecrets;
import static com.nvidia.nvct.service.task.TaskMapperService.DEFAULT_TERMINATION_GRACE_PERIOD_DURATION;
import static com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum.LOGS;
import static com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum.METRICS;
import static com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum.TRACES;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;
import static java.lang.String.format;

import tools.jackson.databind.json.JsonMapper;
import com.nimbusds.oauth2.sdk.util.CollectionUtils;
import com.nimbusds.oauth2.sdk.util.MapUtils;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.BasicTaskDto;
import com.nvidia.nvct.rest.task.dto.CreateTaskRequest;
import com.nvidia.nvct.rest.task.dto.InstanceUsageTypeEnum;
import com.nvidia.nvct.rest.task.dto.ResultHandlingStrategyEnum;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.rest.task.dto.TaskDto;
import com.nvidia.nvct.rest.task.dto.TaskStatusEnum;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.account.dto.AccountDto;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.event.EventService;
import com.nvidia.nvct.service.icms.IcmsClusterGroupClient;
import com.nvidia.nvct.service.icms.IcmsService;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse.ClusterGroup;
import com.nvidia.nvct.service.icms.IcmsStubService.ClusterGroupsResponse.ClusterGroup.Gpu;
import com.nvidia.nvct.service.instance.InstanceService;
import com.nvidia.nvct.service.ngc.NgcRegistryClient;
import com.nvidia.nvct.service.registry.RegistryArtifactService;
import com.nvidia.nvct.service.result.ResultService;
import com.nvidia.nvct.service.reval.RevalClient;
import com.nvidia.nvct.service.task.TaskService.TasksSliceContext;
import com.nvidia.nvct.service.telemetry.dto.TelemetryDto;
import com.nvidia.nvct.service.telemetry.dto.TelemetryTypeEnum;
import java.time.Duration;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.BooleanSupplier;
import java.util.function.Predicate;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class TaskManagementService {

    private static final Duration GFN_MAX_RUNTIME_DURATION = Duration.ofHours(8);
    private static final Pattern RESULTS_LOCATION_PATTERN =
            Pattern.compile("^[^/]+/([^/]+/)?[^/]+[a-zA-Z0-9\\-]*$");

    private static final String MESG_CREATE_AND_LAUNCH_TASK =
            "Account '{}': Creating and launching new task with name '{}'";
    private static final String MESG_ACCOUNT_OPERATION =
            "Account '{}': {}";
    private static final String MESG_TASK_OPERATION =
            "Task id '{}': {}";
    private static final String MESG_TASK_INFO =
            "Task id '{}': {}";

    private static final String MESG_BLANK_PARAMETER =
            "'%s' cannot be blank";
    private static final String MESG_CANNOT_BE_NULL =
            "Parameter '%s' cannot be null";
    private static final String MESG_INVALID_MAX_RUNTIME_DURATION =
            "Invalid request: 'maxRuntimeDuration' cannot exceed PT8H for launching Task on 'GFN'";
    private static final String MESG_MISSING_MAX_RUNTIME_DURATION =
            "Invalid request: 'maxRuntimeDuration' must be specified when launching Task on 'GFN'";
    private static final String MESG_INVALID_GRACE_PERIOD_DURATION =
            "Invalid request: 'terminationGracePeriodDuration' cannot exceed 'maxRuntimeDuration'";
    private static final String MESG_DUPLICATE_SECRETS =
            "Invalid request: Duplicate secrets keys in the payload";
    private static final String MESG_NO_NGC_API_KEY =
            "Invalid request: Missing 'NGC_API_KEY' secret key in the payload";
    private static final String MESG_INVALID_CLUSTER_GROUP =
            "Invalid request: Invalid Backend '%s' specified";
    private static final String MESG_GPUS_MISSING =
            "Invalid request: GPUs missing for Backend '%s'";
    private static final String MESG_INVALID_GPU =
            "Invalid request: Invalid GPU '%s' specified";
    private static final String MESG_INVALID_INSTANCE_TYPE =
            "Invalid request: Invalid InstanceType '%s' specified";
    private static final String MESG_MISSING_RESULTS_LOCATION =
            "Invalid request: Missing 'resultsLocation' property";
    private static final String MESG_INVALID_RESULTS_PATH =
            "Invalid request: 'resultsLocation' should have format " +
                    "'<org-name>/optional-team-name/<model-name>'";
    private static final String MESG_MISSING_SECRETS =
            "Invalid request: Missing secrets with  default or UPLOAD 'resultHandlingStrategy'";
    private static final String MESG_INVALID_DURATION =
            "Invalid request: '%s' cannot be of zero length";
    private static final String MESG_CANCEL_TERMINAL =
            "Invalid request: Cannot cancel a Task with '%s' terminal status";
    private static final String MESG_MISSING_REQ_PROPS =
            "Invalid request: One of the following properties 'containerImage' " +
                    "or 'helmChart' must be specified in the payload";
    private static final String MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION =
            "Invalid request: Cannot specify both '%s' and '%s' properties in the payload";
    private static final String MESG_TELEMETRY_NOT_AVAILABLE =
            "Invalid request: Telemetry '%s' not found in account '%s'";
    private static final String MESG_TELEMETRY_INVALID_TYPE =
            "Invalid request: Telemetry '%s' - Invalid type '%s'";
    private static final String MESG_CONFIGURATION_NOT_EMPTY =
            "Invalid request: Container-based Task should have empty " +
                    "configuration field in GPU specification";
    private static final String MESG_HELM_VALIDATION_POLICY_NOT_SUPPORTED =
            "Invalid request: Container-based Task should have empty " +
                    "helmValidationPolicy field in GPU specification";
    private static final String MESG_MAX_ALLOWED_EXCEEDED =
            "Account '%s': Reached or exceeded the limit for max number of in-flight Tasks '%d'";
    private static final String MESG_FORBIDDEN_TO_CREATE_TASK =
            "Forbidden to create task";
    private static final String MESG_FORBIDDEN_TO_RETRIEVE_TASK =
            "Insufficient resource access for task details";
    private static final String MESG_FORBIDDEN_TO_CANCEL_TASK =
            "Insufficient resource access to cancel task";
    private static final String MESG_FORBIDDEN_TO_DELETE_TASK =
            "Insufficient resource access to delete task";

    private static final String PROP_CONTAINER_IMAGE = "containerImage";
    private static final String PROP_CONTAINER_ARGS = "containerArgs";
    private static final String PROP_CONTAINER_ENVIRONMENT = "containerEnvironment";
    private static final String PROP_HELM_CHART = "helmChart";

    private final AccountService accountService;
    private final EventService eventService;
    private final ResultService resultService;
    private final IcmsService icmsService;
    private final IcmsClusterGroupClient icmsClusterGroupClient;
    private final TaskAuditService taskAuditService;
    private final TaskMapperService taskMapperService;
    private final TaskService taskService;
    private final EssService essService;
    private final InstanceService instanceService;
    private final NgcRegistryClient ngcRegistryClient;
    private final JsonMapper jsonMapper;
    private final RevalClient revalClient;
    private final RegistryArtifactService registryArtifactService;

    @SuppressWarnings("checkstyle:VariableDeclarationUsageDistance")
    public TaskDto createAndLaunchTask(
            String ncaId,
            CreateTaskRequest request,
            BooleanSupplier taskAccessMatch,
            AuditEventPayload.Builder payloadBuilder) {
        log.info(MESG_CREATE_AND_LAUNCH_TASK, ncaId, request.name());
        if (!taskAccessMatch.getAsBoolean()) {
            throw new ForbiddenException(MESG_FORBIDDEN_TO_CREATE_TASK);
        }
        var taskEntity = validateCreateTaskRequest(ncaId, request);
        var taskId = taskEntity.getTaskId();

        Optional<Set<String>> secretNames = Optional.empty();
        try {
            if (!CollectionUtils.isEmpty(request.secrets())) {
                secretNames = saveSecrets(request.secrets(), taskId);
                taskEntity.setHasSecrets(true);
            }
            taskService.saveTask(taskEntity);
            icmsService.scheduleInstance(taskEntity);
        } catch (Exception ex) {
            // Delete secrets from ESS and Task from the NVCT DB.
            essService.deleteSecretsPath(taskId);
            taskService.deleteTask(taskId);
            log.error(ex.getMessage());
            throw ex;
        }

        taskAuditService.auditTaskCreate(payloadBuilder, taskEntity);
        log.info(MESG_TASK_OPERATION, taskId, "Created");
        return taskMapperService.toTaskDto(taskEntity, secretNames, Optional.empty());
    }

    public TasksSliceContext listTasks(
            String ncaId,
            TaskStatusEnum status,
            int limit,
            String cursor,
            Predicate<TaskEntity> taskFilter) {
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        accountService.lookupAccountUsingNcaIdOrThrow(ncaId);

        log.debug(MESG_ACCOUNT_OPERATION, ncaId, "Listing tasks");
        var sliceContext = taskService.fetchTasksByAccount(ncaId, limit, status, cursor, taskFilter);
        log.debug(MESG_ACCOUNT_OPERATION, ncaId, "Listed tasks");

        return sliceContext;
    }

    public TaskDto getTaskDetails(
            String ncaId,
            UUID taskId,
            boolean includeSecrets,
            Predicate<TaskEntity> taskAccessMatch) {
        log.debug(MESG_TASK_OPERATION, taskId, "Retrieving");
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        
        var filteredTask = Optional.of(taskEntity)
                .filter(taskAccessMatch)
                .orElseThrow(() -> new ForbiddenException(MESG_FORBIDDEN_TO_RETRIEVE_TASK));
        var hasSecrets = filteredTask.hasSecrets();
        var secretsNames = includeSecrets && hasSecrets ? essService.getSecretNames(taskId) :
                                                          Optional.<Set<String>>empty();
        var instances = instanceService.getInstances(filteredTask);
        var dto = taskMapperService.toTaskDto(filteredTask, secretsNames, instances);
        log.debug(MESG_TASK_OPERATION, taskId, "Retrieved");
        return dto;
    }

    public BasicTaskDto getBasicTaskDetails(
            String ncaId,
            UUID taskId,
            Predicate<TaskEntity> taskAccessMatch) {
        log.debug(MESG_TASK_OPERATION, taskId, "Retrieving");
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        
        // Filter by predicate - if denied, throw ForbiddenException
        var filteredTask = Optional.of(taskEntity)
                .filter(taskAccessMatch)
                .orElseThrow(() -> new ForbiddenException(MESG_FORBIDDEN_TO_RETRIEVE_TASK));
        
        var dto = taskMapperService.toBasicTaskDto(filteredTask);
        log.debug(MESG_TASK_OPERATION, taskId, "Retrieved");
        return dto;
    }

    public TaskDto cancelTask(
            String ncaId,
            UUID taskId,
            Predicate<TaskEntity> taskAccessMatch,
            AuditEventPayload.Builder payloadBuilder) {
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        if (!taskAccessMatch.test(taskEntity)) {
            throw new ForbiddenException(MESG_FORBIDDEN_TO_CANCEL_TASK);
        }
        var status = taskEntity.getStatus();
        if (TERMINAL_TASK_STATUSES.contains(status)) {
            var mesg = MESG_CANCEL_TERMINAL.formatted(status.name());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var jsonBefore = jsonMapper.valueToTree(taskEntity);
        log.info(MESG_TASK_OPERATION, taskId, "Canceling");
        icmsService.terminateInstanceByTaskId(ncaId, taskId);

        taskEntity.setStatus(TaskStatus.CANCELED);
        taskService.updateTask(taskEntity, false);  // Don't audit - audited by cancelTask

        // Record the event for the change in the status.
        var mesg = STATUS_CHANGE_EVENT_MESSAGE.formatted(status, taskEntity.getStatus());
        log.info(MESG_TASK_INFO, taskId, mesg);
        eventService.insertEvent(ncaId, taskId, mesg);

        taskAuditService.auditTaskCancel(payloadBuilder, jsonBefore, taskEntity);
        var dto = taskMapperService.toTaskDto(taskEntity, Optional.empty(), Optional.empty());
        log.info(MESG_TASK_OPERATION, taskId, "Canceled");
        return dto;
    }

    public boolean deleteTask(
            String ncaId,
            UUID taskId,
            Predicate<TaskEntity> taskAccessMatch,
            AuditEventPayload.Builder payloadBuilder) {
        var taskEntity = taskService.validateAccount(ncaId, taskId);
        if (!taskAccessMatch.test(taskEntity)) {
            log.error(MESG_FORBIDDEN_TO_DELETE_TASK);
            throw new ForbiddenException(MESG_FORBIDDEN_TO_DELETE_TASK);
        }

        log.info(MESG_TASK_OPERATION, taskId, "Deleting");
        eventService.deleteEvents(ncaId, taskId);
        resultService.deleteResults(ncaId, taskId);
        icmsService.terminateInstanceByTaskId(ncaId, taskId);
        essService.deleteSecretsPath(taskId);
        taskService.deleteTask(taskId);

        taskAuditService.auditTaskDelete(payloadBuilder, taskEntity);
        log.info(MESG_TASK_OPERATION, taskId, "Deleted");
        return true;
    }

    private Optional<Set<String>> saveSecrets(Set<SecretDto> secrets, UUID taskId) {
        if (CollectionUtils.isNotEmpty(secrets)) {
            essService.saveSecrets(taskId, secrets);
            return Optional.of(secrets.stream()
                                       .map(SecretDto::name)
                                       .collect(Collectors.toSet()));
        }
        return Optional.empty();
    }

    private TaskEntity validateCreateTaskRequest(String ncaId, CreateTaskRequest request) {
        Objects.requireNonNull(request, () -> MESG_CANNOT_BE_NULL.formatted("request"));
        if (StringUtils.isBlank(ncaId)) {
            var mesg = MESG_BLANK_PARAMETER.formatted("ncaId");
            log.error(mesg);
            throw new IllegalArgumentException(mesg);
        }

        // Validate there is an account corresponding to the specified NCA Id.
        var account = accountService.lookupAccountUsingNcaIdOrThrow(ncaId);
        // validateLimitForMaxTasksAllowed(ncaId, account);  // Limit not enforced for now.
        validateDurations(request);
        validateGpuSpecification(ncaId, request);
        validateSecrets(request);
        validateResultHandlingStrategy(request);
        validateContainer(request);
        validateTelemetries(ncaId, request);

        // Validate artifacts before validating the helm chart using Reval service.
        var taskEntity = taskMapperService.toTaskEntity(ncaId, request);
        registryArtifactService.validateArtifacts(taskEntity);
        validateHelmChart(ncaId, request, taskEntity);
        return taskEntity;
    }

    // During load testing, this method seemed to be causing a bottleneck as we would fetch
    // all the Tasks from the DB and filter them for each new Task that is being created. As
    // the number of Tasks in an account increases, this method will start taking more and
    // more time. Till we have a good solution, we will not call this method and not enforce
    // the limit.
    private void validateLimitForMaxTasksAllowed(String ncaId, AccountDto account) {
        // A new Task is going to be created, check if total number of in-flight
        // Tasks under the specified account is smaller than threshold.
        var inFlightTaskCount = taskService.fetchTasksByAccount(ncaId)
                .filter(task -> !TERMINAL_TASK_STATUSES.contains(task.getStatus()))
                .count();
        if (inFlightTaskCount >= account.maxTasksAllowed()) {
            var msg = format(MESG_MAX_ALLOWED_EXCEEDED, ncaId, account.maxTasksAllowed());
            log.error(msg);
            throw new BadRequestException(msg);
        }
    }

    private static void validateDurations(CreateTaskRequest request) {
        if ((request.maxRuntimeDuration() != null) && request.maxRuntimeDuration().isZero()) {
            var mesg = MESG_INVALID_DURATION.formatted("maxRuntimeDuration");
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if ((request.terminationGracePeriodDuration() != null)
                && request.terminationGracePeriodDuration().isZero()) {
            var mesg = MESG_INVALID_DURATION.formatted("terminationGracePeriodDuration");
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if ((request.maxQueuedDuration() != null) && request.maxQueuedDuration().isZero()) {
            var mesg = MESG_INVALID_DURATION.formatted("maxQueuedDuration");
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var maxRuntimeDuration = request.maxRuntimeDuration();
        var terminationGracePeriodDuration =
                Objects.requireNonNullElse(request.terminationGracePeriodDuration(),
                                           DEFAULT_TERMINATION_GRACE_PERIOD_DURATION);

        if ((maxRuntimeDuration != null)
                && maxRuntimeDuration.minus(terminationGracePeriodDuration).isNegative()) {
            log.error(MESG_INVALID_GRACE_PERIOD_DURATION);
            throw new BadRequestException(MESG_INVALID_GRACE_PERIOD_DURATION);
        }
    }

    private void validateGpuSpecification(String ncaId, CreateTaskRequest request) {
        var spec = request.gpuSpecification();
        var maxRuntimeDuration = request.maxRuntimeDuration();
        List<Gpu> gpus;
        var backend = spec.backend();
        var clusterGroups = icmsClusterGroupClient.getClusterGroups(
                ncaId, getTaskContainerType(request));

        if (StringUtils.isNotBlank(backend)) {
            if (backend.equals("GFN")) {
                if (maxRuntimeDuration == null) {
                    log.error(MESG_MISSING_MAX_RUNTIME_DURATION);
                    throw new BadRequestException(MESG_MISSING_MAX_RUNTIME_DURATION);
                }

                if (GFN_MAX_RUNTIME_DURATION.minus(maxRuntimeDuration).isNegative()) {
                    log.error(MESG_INVALID_MAX_RUNTIME_DURATION);
                    throw new BadRequestException(MESG_INVALID_MAX_RUNTIME_DURATION);
                }
            }

            var clusterGroup = clusterGroups.stream()
                    .filter(cg -> cg.getName().equalsIgnoreCase(backend))
                    .findAny()
                    .orElseThrow(() -> {
                        var mesg = MESG_INVALID_CLUSTER_GROUP.formatted(backend);
                        log.error(mesg);
                        return new BadRequestException(mesg);
                    });
            if (CollectionUtils.isEmpty(clusterGroup.getGpus())) {
                var mesg = MESG_GPUS_MISSING.formatted(backend);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
            gpus = clusterGroup.getGpus().stream()
                    .filter(gpu -> gpu.getName().equalsIgnoreCase(spec.gpu()))
                    .toList();

        } else {
            gpus = clusterGroups.stream()
                    .map(ClusterGroup::getGpus)
                    .flatMap(Collection::stream)
                    .filter(gpu -> gpu.getName().equalsIgnoreCase(spec.gpu()))
                    .toList();
        }

        if (CollectionUtils.isEmpty(gpus)) {
            var mesg = MESG_INVALID_GPU.formatted(spec.gpu());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var instanceType = spec.instanceType();
        if (StringUtils.isNotBlank(instanceType)
                && gpus.stream()
                .map(ClusterGroup.Gpu::getInstanceTypes)
                .flatMap(Collection::stream)
                .noneMatch(it -> it.getName().equalsIgnoreCase(instanceType))) {
            var mesg = MESG_INVALID_INSTANCE_TYPE.formatted(spec.instanceType());
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    private void validateResultHandlingStrategy(CreateTaskRequest request) {
        var secrets = request.secrets();
        var resultHandlingStrategy = request.resultHandlingStrategy();

        if ((resultHandlingStrategy == null)
                || resultHandlingStrategy == ResultHandlingStrategyEnum.UPLOAD) {
            if (CollectionUtils.isEmpty(secrets)) {
                log.error(MESG_MISSING_SECRETS);
                throw new BadRequestException(MESG_MISSING_SECRETS);
            }

            var optNgcApiKey = getNgcApiKey(secrets);
            if (CollectionUtils.isEmpty(secrets) || optNgcApiKey.isEmpty()) {
                log.error(MESG_NO_NGC_API_KEY);
                throw new BadRequestException(MESG_NO_NGC_API_KEY);
            }

            var resultsLocation = request.resultsLocation();
            if (StringUtils.isBlank(resultsLocation)) {
                log.error(MESG_MISSING_RESULTS_LOCATION);
                throw new BadRequestException(MESG_MISSING_RESULTS_LOCATION);
            }

            if (!RESULTS_LOCATION_PATTERN.matcher(resultsLocation).matches()) {
                log.error(MESG_INVALID_RESULTS_PATH);
                throw new BadRequestException(MESG_INVALID_RESULTS_PATH);
            }

            // Validate whether the specified NGC_API_KEY can be used to create results
            // or checkpoint models at the specified resultsLocation.
            var ngcApiKey = optNgcApiKey.get();
            ngcRegistryClient.validate(ngcApiKey, resultsLocation);
        }
    }

    private static void validateSecrets(CreateTaskRequest request) {
        var secrets = request.secrets();
        if (CollectionUtils.isNotEmpty(secrets) && hasDupeSecrets(secrets)) {
            log.error(MESG_DUPLICATE_SECRETS);
            throw new BadRequestException(MESG_DUPLICATE_SECRETS);
        }
    }

    private void validateHelmChart(
            String ncaId,
            CreateTaskRequest request,
            TaskEntity task) {
        var helmChart = request.helmChart();

        if (helmChart == null && request.containerImage() == null) {
            log.error(MESG_MISSING_REQ_PROPS);
            throw new BadRequestException(MESG_MISSING_REQ_PROPS);
        }

        if (helmChart != null && request.containerImage() != null) {
            var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                    .formatted(PROP_CONTAINER_IMAGE, PROP_HELM_CHART);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (helmChart != null && StringUtils.isNotBlank(request.containerArgs())) {
            var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                    .formatted(PROP_CONTAINER_ARGS, PROP_HELM_CHART);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (helmChart != null && request.containerEnvironment() != null) {
            var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                    .formatted(PROP_CONTAINER_ENVIRONMENT, PROP_HELM_CHART);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        if (helmChart != null) {
            var spec = request.gpuSpecification();
            var validationErrorMsg = revalClient.validate(ncaId, task, spec);

            if (StringUtils.isBlank(validationErrorMsg)) {
                return;
            }

            log.error(validationErrorMsg);
            throw new BadRequestException(validationErrorMsg);
        }
    }

    // Container based Task should have empty configuration and helmValidationPolicy
    // fields in gpuSpec.
    private static void validateContainer(CreateTaskRequest request) {
        var helmChart = request.helmChart();
        if (helmChart == null) {
            var spec = request.gpuSpecification();
            if (spec.configuration() != null
                    && StringUtils.isNotBlank(spec.configuration().toString())) {
                var mesg = MESG_CONFIGURATION_NOT_EMPTY;
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
            if (spec.helmValidationPolicy() != null) {
                var mesg = MESG_HELM_VALIDATION_POLICY_NOT_SUPPORTED;
                log.error(mesg);
                throw new BadRequestException(mesg);
            }
        }
    }

    private void validateTelemetries(String ncaId, CreateTaskRequest request) {
        var telemetriesDto = request.telemetries();
        if (telemetriesDto == null) {
            return;
        }

        // Invalidate cache for the specified account to fetch any new telemetries that may
        // be defined before the Task is created.
        accountService.invalidateCacheForSpecificAccount(ncaId);
        var telemetryMap = accountService.getAccountTelemetryMap(ncaId);

        if (telemetriesDto.logsTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetryMap, telemetriesDto.logsTelemetryId(), LOGS);
        }

        if (telemetriesDto.metricsTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetryMap, telemetriesDto.metricsTelemetryId(), METRICS);
        }

        if (telemetriesDto.tracesTelemetryId() != null) {
            validateTelemetryType(ncaId, telemetryMap, telemetriesDto.tracesTelemetryId(), TRACES);
        }
    }

    private static void validateTelemetryType(
            String ncaId,
            Map<UUID, TelemetryDto> telemetryMap,
            UUID telemetryId,
            TelemetryTypeEnum expectedType) {
        if (telemetryId == null) {
            return;
        }

        if (MapUtils.isEmpty(telemetryMap) || !telemetryMap.containsKey(telemetryId)) {
            var mesg = MESG_TELEMETRY_NOT_AVAILABLE.formatted(telemetryId, ncaId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        var telemetryDto = telemetryMap.get(telemetryId);
        if (!telemetryDto.types().contains(expectedType)) {
            var mesg = MESG_TELEMETRY_INVALID_TYPE.formatted(telemetryId, expectedType);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
    }

    // Determines if the instance-type can be used to specifically launch just a
    // Container-based Task or any(Container / Helm) Task.
    private static InstanceUsageTypeEnum getTaskContainerType(CreateTaskRequest request) {
        if (Objects.nonNull(request.containerImage())) {
            return InstanceUsageTypeEnum.CONTAINER;
        }
        return InstanceUsageTypeEnum.DEFAULT;
    }
}
