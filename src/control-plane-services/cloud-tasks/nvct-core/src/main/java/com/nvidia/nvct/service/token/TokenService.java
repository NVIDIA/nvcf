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
package com.nvidia.nvct.service.token;

import com.nvidia.boot.exceptions.UnauthorizedException;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.service.account.AccountService;
import com.nvidia.nvct.service.token.client.NotaryClient;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RefreshScope
public class TokenService {
    private static final String MESG_MISSING_TOKEN = "Missing token";

    private final NotaryClient notaryClient;
    private final AccountService accountService;
    private final WorkerAssertionValidator workerAssertionValidator;

    public TokenService(
            NotaryClient notaryClient,
            AccountService accountService,
            WorkerAssertionValidator workerAssertionValidator) {
        this.notaryClient = notaryClient;
        this.accountService = accountService;
        this.workerAssertionValidator = workerAssertionValidator;
    }

    public String issueSecretsAssertion(TaskEntity task) {
        var taskId = task.getTaskId();
        var ncaId = task.getNcaId();
        var taskSecretsPresent = task.hasSecrets();
        var telemetries = task.getTelemetries();
        var telemetrySecretsPresent = (telemetries != null);

        if (telemetrySecretsPresent && taskSecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(ncaId, taskId, telemetries);
        } else if (telemetrySecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(ncaId, telemetries);
        } else if (taskSecretsPresent) {
            return notaryClient.issueSecretPathsAssertionToken(taskId);
        }

        return StringUtils.EMPTY;
    }

    public String issueWorkerAccessAssertion(String ncaId, UUID taskId) {
        accountService.getAccountName(ncaId);
        return notaryClient.issueWorkerAccessAssertionToken(ncaId, taskId);
    }

    public void validateWorkerAccessAssertion(String ncaId, UUID taskId) {
        accountService.getAccountName(ncaId);
        workerAssertionValidator.validate(getAccessToken(), ncaId, taskId);
    }

    private static String getAccessToken() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        if (authentication == null || !(authentication.getCredentials() instanceof String token)) {
            log.error(MESG_MISSING_TOKEN);
            throw new UnauthorizedException(MESG_MISSING_TOKEN);
        }
        return token;
    }

}
