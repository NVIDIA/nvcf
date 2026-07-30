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
package com.nvidia.nvct.rest.secret;

import static com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy.UPLOAD;
import static com.nvidia.nvct.service.secret.SecretManagementService.getNgcApiKey;
import static com.nvidia.nvct.service.secret.SecretManagementService.hasDupeSecrets;
import static com.nvidia.nvct.service.task.TaskPredicateUtils.taskAccessMatch;
import static com.nvidia.nvct.util.NvctConstants.TERMINAL_TASK_STATUSES;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.ess.EssService;
import com.nvidia.nvct.service.ngc.NgcRegistryClient;
import com.nvidia.nvct.service.task.TaskService;
import java.util.HashSet;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Function;
import java.util.stream.Collectors;
import java.util.stream.Stream;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.ResponseEntity;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;
import org.springframework.util.CollectionUtils;

@Slf4j
@Service
@RequiredArgsConstructor
public class SecretManagementFacade {

    private static final String MESG_MISSING_SECRETS =
            "Task id '%s': Missing secrets in the payload";
    private static final String MESG_DUPLICATE_SECRETS =
            "Task id '%s': Duplicate secrets keys in the payload";
    private static final String MESG_INVALID_OPERATION =
            "Task id '%s': Cannot update secrets for a Task that does not have any secrets";
    private static final String MESG_TERMINAL_TASK_SECRETS =
            "Task id '%s': Cannot update secrets for a Task with terminal status '%s'";
    private static final String MESG_TASK_NOT_IN_ACCOUNT =
            "Task id '%s': Not found in account '%s'";
    private static final String MESG_FORBIDDEN_TO_UPDATE_SECRETS =
            "Task '%s': Forbidden to update secrets";

    private final EssService essService;
    private final NgcRegistryClient ngcRegistryClient;
    private final TaskService taskService;
    private final AccountService accountService;

    public ResponseEntity<Void> updateSecrets(
            String ncaId,
            UUID taskId,
            Set<SecretDto> secrets,
            Authentication authentication) {
        if (CollectionUtils.isEmpty(secrets)) {
            var mesg = MESG_MISSING_SECRETS.formatted(taskId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        // Confirm that the account exists.
        accountService.lookupAccountUsingNcaIdOrThrow(ncaId);

        var taskEntity = taskService.fetchTask(taskId);

        if (!taskAccessMatch(authentication, Optional.of(taskId))) {
            var mesg = MESG_FORBIDDEN_TO_UPDATE_SECRETS.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        // If a Task does not have any secrets, then we do not allow secrets to be updated.
        if (!taskEntity.hasSecrets()) {
            var mesg = MESG_INVALID_OPERATION.formatted(taskId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // Confirm Task belongs to the specified ncaId/account.
        if (!taskEntity.getNcaId().equals(ncaId)) {
            var mesg = MESG_TASK_NOT_IN_ACCOUNT.formatted(taskId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        // Cannot update secrets for a Task with terminal status.
        var status = taskEntity.getStatus();
        if (TERMINAL_TASK_STATUSES.contains(status)) {
            var mesg = MESG_TERMINAL_TASK_SECRETS.formatted(taskId, status);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        validateSecrets(taskEntity, secrets);

        var existingSecrets = essService.getSecrets(taskId)
                .orElseGet(Set::of)
                .stream()
                .collect(Collectors.toMap(SecretDto::name, Function.identity()));

        // Get the existing secrets first and then merge them with the new secrets
        // Preferring new secrets values if the secret name is the same.
        var newSecrets = secrets.stream()
                .collect(Collectors.toMap(SecretDto::name, Function.identity()));
        var mergedSecrets = Stream.concat(existingSecrets.entrySet().stream(),
                                          newSecrets.entrySet().stream())
                .collect(Collectors.toMap(
                        Map.Entry::getKey,
                        Map.Entry::getValue,
                        (oldValue, newValue) -> newValue));
        essService.saveSecrets(taskId, new HashSet<>(mergedSecrets.values()));
        return ResponseEntity.noContent().build();    // Status 204.
    }

    private void validateSecrets(
            TaskEntity taskEntity,
            Set<SecretDto> secrets) {
        var taskId = taskEntity.getTaskId();
        if (hasDupeSecrets(secrets)) {
            var mesg = MESG_DUPLICATE_SECRETS.formatted(taskId);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }

        // If a new NGC_API_KEY is being provided when updating secrets, then make sure that
        // it can be used to upload results/checkpoints at the specified resultsLocation by
        // creating and deleting a dummy/empty model. If successful, then the new NGC_API_KEY
        // is valid. Otherwise, we respond with a 400.
        var ngcApiKey = getNgcApiKey(secrets);
        if (ngcApiKey.isPresent()) {
            var resultsLocation = taskEntity.getResultsLocation();
            var resultHandlingStrategy = taskEntity.getResultHandlingStrategy();

            if (resultHandlingStrategy == UPLOAD && StringUtils.isNotBlank(resultsLocation)) {
                // If resultHandlingStrategy is UPLOAD, then resultsLocation will not be blank.
                ngcRegistryClient.validate(ngcApiKey.get(), resultsLocation);
            }
        }
    }
}
