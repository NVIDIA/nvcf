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

import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.nvct.service.token.client.NotaryStubService.WorkerAccessAssertion;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Map;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtException;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class WorkerAssertionValidator {

    private static final String MESG_INVALID_SIGNATURE =
            "Task id '%s': Invalid worker assertion token";
    private static final String MESG_INVALID_ISSUER =
            "Task id '%s': Invalid or missing issuer in worker assertion token";
    private static final String MESG_EXPIRED_TOKEN =
            "Task id '%s': Expired worker assertion token";
    private static final String MESG_INVALID_SUBJECT =
            "Task id '%s': Invalid or missing subject in worker assertion token";
    private static final String MESG_INVALID_ASSERTION =
            "Task id '%s': Invalid or missing assertion claim in worker assertion token";
    private static final String MESG_MISMATCH_NCA_ID =
            "NCA id '%s': Does not match the one in the access token";
    private static final String MESG_MISMATCH_TASK_ID =
            "Task id '%s': Does not match the one in the access token";
    private static final String MESG_DELEGATED_DISABLED =
            "Task id '%s': Delegated worker tokens are not enabled";
    private static final String MESG_DELEGATED_INACTIVE =
            "Task id '%s': worker token not active";
    private static final String MESG_DELEGATED_NOT_BOUND =
            "Task id '%s': worker token not bound to requested task";

    private static final Duration VALIDITY = Duration.ofHours(3);

    private final JwtDecoder jwtDecoder;
    private final JsonMapper jsonMapper;
    private final Clock clock;
    private final String issuer;
    private final String subject;
    private final WorkerTokenIntrospectionService workerTokenIntrospectionService;

    public WorkerAssertionValidator(
            @Qualifier("notaryJwtDecoder") JwtDecoder jwtDecoder,
            JsonMapper jsonMapper,
            Clock clock,
            @Value("${nvct.notary.base-url}") String issuer,
            @Value("${spring.security.oauth2.client.registration.notary.client-id}")
            String subject,
            WorkerTokenIntrospectionService workerTokenIntrospectionService) {
        this.jwtDecoder = jwtDecoder;
        this.jsonMapper = jsonMapper;
        this.clock = clock;
        this.issuer = issuer;
        this.subject = subject;
        this.workerTokenIntrospectionService = workerTokenIntrospectionService;
    }

    /**
     * Validates the worker's bearer token for the given task. The path is chosen by token
     * shape: a delegated token (compact JWS with an {@code nvcf-icms:} audience) is
     * introspected through ICMS and its task binding must equal the requested task; any other
     * token is a legacy Notary assertion and is validated locally. A failure on one path never
     * falls through to the other.
     *
     * @return true when the caller authenticated with a delegated token
     */
    public boolean validate(String token, String ncaId, UUID taskId) {
        if (!WorkerTokenIntrospectionService.isDelegatedToken(token)) {
            validateNotaryJwt(token, ncaId, taskId);
            return false;
        }
        if (!workerTokenIntrospectionService.isEnabled()) {
            var mesg = MESG_DELEGATED_DISABLED.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        var result = workerTokenIntrospectionService.introspect(token);
        if (!result.isActive()) {
            log.warn("worker token introspection returned active=false: {}", result.getError());
            throw new ForbiddenException(MESG_DELEGATED_INACTIVE.formatted(taskId));
        }
        if (!ncaId.equals(result.getNcaId()) || !taskId.equals(parseTaskId(result.getTaskId()))) {
            log.error("delegated worker token bound to task {} / nca {} presented for task {} / nca {}",
                      result.getTaskId(), result.getNcaId(), taskId, ncaId);
            throw new ForbiddenException(MESG_DELEGATED_NOT_BOUND.formatted(taskId));
        }
        log.debug("task worker authorized via delegated token, instance_id={}", result.getInstanceId());
        return true;
    }

    private static UUID parseTaskId(String taskId) {
        try {
            return UUID.fromString(taskId);
        } catch (IllegalArgumentException | NullPointerException e) {
            return null;
        }
    }

    private void validateNotaryJwt(String token, String ncaId, UUID taskId) {
        var jwt = decode(token, taskId);
        validateIssuer(jwt, taskId);
        validateIssuedAt(jwt, taskId);
        validateSubject(jwt, taskId);
        var accessAssertion = getAssertion(jwt, taskId);

        if (!accessAssertion.ncaId().equals(ncaId)) {
            var mesg = MESG_MISMATCH_NCA_ID.formatted(ncaId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        if (!accessAssertion.taskId().equals(taskId)) {
            var mesg = MESG_MISMATCH_TASK_ID.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
    }

    private Jwt decode(String token, UUID taskId) {
        try {
            return jwtDecoder.decode(token);
        } catch (JwtException ex) {
            var mesg = MESG_INVALID_SIGNATURE.formatted(taskId);
            log.error(mesg, ex);
            throw new ForbiddenException(mesg, ex);
        }
    }

    private void validateIssuer(Jwt jwt, UUID taskId) {
        if (jwt.getIssuer() == null || !issuer.equals(jwt.getIssuer().toString())) {
            var mesg = MESG_INVALID_ISSUER.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
    }

    private void validateIssuedAt(Jwt jwt, UUID taskId) {
        var issuedAt = jwt.getIssuedAt();
        if (issuedAt == null) {
            var mesg = MESG_EXPIRED_TOKEN.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
        if (issuedAt.plus(VALIDITY).isBefore(Instant.now(clock))) {
            var mesg = MESG_EXPIRED_TOKEN.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
    }

    private void validateSubject(Jwt jwt, UUID taskId) {
        if (StringUtils.isBlank(jwt.getSubject()) || !jwt.getSubject().equals(subject)) {
            var mesg = MESG_INVALID_SUBJECT.formatted(taskId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }
    }

    private WorkerAccessAssertion getAssertion(Jwt jwt, UUID taskId) {
        try {
            var assertionClaim = jwt.getClaim("assertion");
            if (!(assertionClaim instanceof Map<?, ?> assertionMap)) {
                var mesg = MESG_INVALID_ASSERTION.formatted(taskId);
                log.error(mesg);
                throw new ForbiddenException(mesg);
            }
            var assertionJson = jsonMapper.writeValueAsString(assertionMap);
            return jsonMapper.readValue(assertionJson, WorkerAccessAssertion.class);
        } catch (JacksonException | IllegalArgumentException ex) {
            var mesg = MESG_INVALID_ASSERTION.formatted(taskId);
            log.error(mesg, ex);
            throw new ForbiddenException(mesg, ex);
        }
    }
}
