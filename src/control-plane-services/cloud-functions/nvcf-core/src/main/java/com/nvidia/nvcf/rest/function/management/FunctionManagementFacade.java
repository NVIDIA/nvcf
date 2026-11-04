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
package com.nvidia.nvcf.rest.function.management;

import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.authorizedFunctionMatch;
import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.createFunctionMatch;
import static com.nvidia.nvcf.service.function.FunctionPredicateUtils.privateFunctionMatch;
import static com.nvidia.nvcf.util.NvcfConstants.SCOPE_LIST_FUNCTIONS_DETAILS;

import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionIdsResponse;
import com.nvidia.nvcf.rest.function.management.dto.ListFunctionsResponse;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.VisibilityEnum;
import com.nvidia.nvcf.service.function.FunctionAuditService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionManagementService;
import com.nvidia.nvcf.util.NvcfUtils;
import jakarta.servlet.http.HttpServletRequest;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class FunctionManagementFacade {
    private final FunctionManagementService functionManagementService;
    private final FunctionAuditService functionAuditService;
    private final FunctionLookupService functionLookupService;

    public CreateFunctionResponse createFunction(
            String ncaId,
            Optional<UUID> optFunctionId,
            CreateFunctionRequest createRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var optFunction = optFunctionId.map(functionLookupService::lookupFirstUsingFunctionId);
        var functionDto = functionManagementService
                .registerFunction(ncaId,
                                  optFunctionId,
                                  createRequest,
                                  () -> createFunctionMatch(ncaId, authentication, optFunction),
                                  payloadBuilder);
        return new CreateFunctionResponse(functionDto);
    }

    public ListFunctionsResponse getFunctions(
            String ncaId,
            Authentication authentication,
            Set<VisibilityEnum> visibilities) {
        var dtos = functionManagementService
                .getFunctions(ncaId,
                              function -> privateFunctionMatch(ncaId, authentication, function),
                              function -> authorizedFunctionMatch(ncaId, authentication, function),
                              visibilities)
                .stream()
                .map(dto -> hideDetailsForPublicAndSharedFunctions(ncaId, dto))
                .toList();
        return new ListFunctionsResponse(dtos);
    }

    public ListFunctionsResponse getFunctions(
            String ncaId,
            UUID functionId,
            Authentication authentication) {
        var dtos = functionManagementService
                .getFunctions(ncaId,
                              functionId,
                              function -> privateFunctionMatch(ncaId, authentication, function),
                              function -> authorizedFunctionMatch(ncaId, authentication, function))
                .stream()
                .map(dto -> hideDetailsForPublicAndSharedFunctions(ncaId, dto))
                .toList();
        return new ListFunctionsResponse(dtos);
    }

    public ListFunctionIdsResponse getFunctionIds(
            String ncaId,
            Authentication authentication,
            Set<VisibilityEnum> visibilities) {
        var functionIds = functionManagementService
                .getFunctions(ncaId,
                              function -> privateFunctionMatch(ncaId, authentication, function),
                              function -> authorizedFunctionMatch(ncaId, authentication, function),
                              visibilities)
                .stream()
                .map(FunctionDto::id)
                .distinct()
                .toList();
        return new ListFunctionIdsResponse(functionIds);
    }

    public FunctionResponse getFunction(
            String ncaId,
            UUID functionId,
            UUID versionId,
            boolean includeSecrets,
            Authentication authentication) {
        boolean includeDetails = includeDetails(authentication);
        var dto = functionManagementService
                .getFunction(ncaId,
                             functionId,
                             versionId,
                             function -> privateFunctionMatch(ncaId, authentication, function),
                             function -> authorizedFunctionMatch(ncaId, authentication, function),
                             includeDetails,
                             includeSecrets);
        var updatedDto = hideDetailsForPublicAndSharedFunctions(ncaId, dto);
        return new FunctionResponse(updatedDto);
    }

    public void deleteFunction(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        functionManagementService
                .deleteFunction(ncaId,
                                functionId,
                                functionVersionId,
                                function -> privateFunctionMatch(ncaId, authentication, function),
                                payloadBuilder);
    }

    public FunctionResponse updateFunction(
            String ncaId,
            UUID functionId,
            UUID functionVersionId,
            UpdateFunctionRequest updateRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var payloadBuilder = auditEventPayloadBuilder(httpServletRequest, authentication);
        var dto = functionManagementService
                .updateFunction(ncaId,
                                functionId,
                                functionVersionId,
                                function -> privateFunctionMatch(ncaId, authentication, function),
                                updateRequest,
                                payloadBuilder);
        return new FunctionResponse(dto);
    }

    private AuditEventPayload.Builder auditEventPayloadBuilder(
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        var customProperties = NvcfUtils.getCustomProperties(httpServletRequest);
        return functionAuditService.auditEventPayloadBuilder(authentication, customProperties);
    }

    // Returns true if list_function_details, apikey:list_functions_details, or
    // admin:list_functions_details scopes are specified.
    private static boolean includeDetails(Authentication authentication) {
        return authentication.getAuthorities()
                .stream()
                .map(GrantedAuthority::getAuthority)
                .anyMatch(authority -> authority.endsWith(SCOPE_LIST_FUNCTIONS_DETAILS));
    }

    private static FunctionDto hideDetailsForPublicAndSharedFunctions(
            String ncaId,
            FunctionDto dto) {
        var ownerNcaId = dto.ncaId();
        if (!ownerNcaId.equals(ncaId)) {
            return dto.toBuilder()
                    .ownedByDifferentAccount(true)
                    .inferenceUrl(null)
                    .inferencePort(null)
                    .containerArgs(null)
                    .containerEnvironment(null)
                    .containerImage(null)
                    .models(null)
                    .resources(null)
                    .helmChart(null)
                    .helmChartServiceName(null)
                    .activeInstances(null)
                    .secrets(null)
                    .rateLimit(null)
                    .build();
        }
        return dto;
    }

}
