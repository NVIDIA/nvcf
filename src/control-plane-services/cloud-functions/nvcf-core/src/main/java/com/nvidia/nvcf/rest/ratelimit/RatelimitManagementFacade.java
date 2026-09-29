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
package com.nvidia.nvcf.rest.ratelimit;

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;

import com.nvidia.boot.exceptions.ForbiddenException;
import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterPolicyService;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class RatelimitManagementFacade {

    private static final String MESG_FUNCTION_NOT_FOUND =
            "Function id '%s', version '%s': Not found for account '%s'";
    private static final String MESG_FUNCTION_FORBIDDEN =
            "Function id '%s', version '%s': Forbidden to update ratelimit for this function";

    private final RateLimiterPolicyService rateLimiterPolicyService;
    private final FunctionLookupService functionLookupService;

    public void deleteRateLimit(
            String ncaId,
            UUID functionId,
            UUID versionId,
            Authentication authentication) {
        // Verify function exists and belongs to the account
        var function = functionLookupService
                .lookupUsingFunctionIdAndVersionIdOrThrow(functionId, versionId);
        if (!function.getNcaId().equals(ncaId)) {
            var mesg = MESG_FUNCTION_NOT_FOUND.formatted(functionId, versionId, ncaId);
            log.error(mesg);
            throw new NotFoundException(mesg);
        }

        if (!privateFunctionMatch(ncaId, authentication, function)) {
            var mesg = MESG_FUNCTION_FORBIDDEN.formatted(functionId, versionId);
            log.error(mesg);
            throw new ForbiddenException(mesg);
        }

        rateLimiterPolicyService.deleteRateLimit(function);
    }
}
