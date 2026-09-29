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
package com.nvidia.nvcf.rest.function.invocation;

import static com.nvidia.nvcf.configuration.notary.NotaryAuthManagerConfiguration.VALIDITY;
import static java.lang.String.format;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenRequest;
import com.nvidia.nvcf.rest.function.invocation.dto.InvocationTokenResponse;
import com.nvidia.nvcf.rest.function.invocation.dto.MultiFunctionsInvocationTokenRequest;
import com.nvidia.nvcf.rest.function.management.dto.BasicFunctionDto;
import com.nvidia.nvcf.service.function.invocation.FunctionInvocationValidationService;
import com.nvidia.nvcf.service.token.client.NotaryClient;
import jakarta.annotation.Nullable;
import java.util.List;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionInvocationFacade {

    private static final String MESG_VALIDATED_FUNCTION_ID_ON_RESPONSE_LOOKUP =
            "Function invocation request id '{}': Validated function id '{}' belongs to account '{}'";
    private static final String MESG_INVALID_REQUEST_ID =
            "Function invocation request id '%s': Invalid invocation request id specified";

    private final NotaryClient notaryClient;
    private final FunctionInvocationValidationService functionInvocationValidationService;

    public InvocationTokenResponse issueFunctionInvocationToken(
            InvocationTokenRequest request,
            String ncaId,
            UUID functionId,
            @Nullable UUID versionId,
            Authentication authentication) {
        validateAccess(functionId, versionId, UUID.randomUUID(), ncaId, authentication);
        var newRequest = MultiFunctionsInvocationTokenRequest.builder()
                .functions(List.of(BasicFunctionDto.builder()
                                                   .functionId(functionId)
                                                   .functionVersionId(versionId)
                                                   .build()))
                .clientId(request.clientId())
                .build();
        var token = notaryClient.issueFunctionInvocationAssertionToken(newRequest, ncaId);
        return new InvocationTokenResponse(token, (int) VALIDITY.toSeconds());
    }

    public InvocationTokenResponse issueMultiFunctionsInvocationToken(
            MultiFunctionsInvocationTokenRequest invocationTokenRequest,
            String ncaId,
            Authentication authentication) {
        // Don't validate if ncaId is authorized to invoke the function for linear scaling NVCF-5015
        var token = notaryClient.issueFunctionInvocationAssertionToken(invocationTokenRequest, ncaId);
        return new InvocationTokenResponse(token, (int) VALIDITY.toSeconds());
    }

    public void validateAccess(
            UUID functionId, @Nullable UUID functionVersionId, UUID requestId, String ncaId,
            Authentication authentication) {
        // check for nca id match before waiting for polling and response handling
        // the wrong account owner should not be able to trigger any response
        // consumption or metrics or know if or when the function invocation completed
        var candidateFunction = functionInvocationValidationService.lookupAndValidateAccess(
                authentication, ncaId, functionId, functionVersionId).getFirst();
        if (candidateFunction == null) {
            var mesg = format(MESG_INVALID_REQUEST_ID, requestId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }
        log.debug(MESG_VALIDATED_FUNCTION_ID_ON_RESPONSE_LOOKUP, requestId,
                  candidateFunction.targetFunction().getFunctionId(),
                  candidateFunction.targetFunction().getNcaId());
    }
}
