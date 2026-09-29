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
package com.nvidia.nvcf.rest.function.deployment;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.function.deployment.dto.DeploymentResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.FunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.ListDeploymentsResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateFunctionDeploymentRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationRequest;
import com.nvidia.nvcf.rest.function.deployment.dto.UpdateGpuSpecificationResponse;
import com.nvidia.nvcf.rest.function.management.dto.FunctionResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.function.FunctionDeploymentLookupService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import java.util.Map;
import java.util.UUID;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PatchMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(value = "/v2/nvcf/accounts/{ncaId}", produces = APPLICATION_JSON_VALUE)
@Tag(name = "Cross-Account Function Deployment for NVIDIA Super Admins",
        description = """
                Defines function deployment endpoints for NVIDIA Super Admins to work across
                 accounts. All the endpoints defined in this API require a bearer token
                 with 'admin:deploy_function' scope in the HTTP Authorization header."""
)
public class XAccountFunctionDeploymentController {

    private static final String AUTH_DESCRIPTION = """
            Access to this endpoint mandates a bearer token with 'admin:deploy_function' scope in
             HTTP Authorization header.
            """;
    private static final String NCA_ID_DESCRIPTION = "Id of the NVIDIA Cloud Account";

    private final FunctionDeploymentFacade functionDeploymentFacade;
    private final RateLimiterService rateLimiterService;
    private final AccountService accountService;
    private final FunctionDeploymentLookupService functionDeploymentLookupService;
    private final Tracer tracer;

    @PostMapping(value = "deployments/functions/{functionId}/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Deploy Function",
            description = """
                    Initiates deployment for the specified function version. Upon invocation of
                     this endpoint, the function's status transitions to 'DEPLOYING'.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public DeploymentResponse createFunctionDeployment(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @PathVariable UUID functionVersionId,
            @NotNull @Valid @RequestBody FunctionDeploymentRequest deploymentRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.createFunctionDeployment(ncaId,
                                                                 functionId,
                                                                 functionVersionId,
                                                                 deploymentRequest,
                                                                 httpServletRequest,
                                                                 authentication);
    }

    @DeleteMapping("deployments/functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Delete Function Deployment",
            description = """
                    Deletes the deployment associated with the specified function. Upon
                     deletion, any active instances will be terminated, and the function's status
                     will transition to 'INACTIVE'. To undeploy a function version gracefully,
                     specify 'graceful=true' query parameter, allowing current tasks to complete
                     before terminating the instances.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public FunctionResponse deleteFunctionDeployment(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @PathVariable UUID functionVersionId,
            @Parameter(description = "Query param to deactivate function for graceful shutdown")
            @RequestParam(required = false, defaultValue = "false") boolean graceful,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.deleteFunctionDeployment(ncaId,
                                                                 functionId,
                                                                 functionVersionId,
                                                                 graceful,
                                                                 httpServletRequest,
                                                                 authentication);
    }

    @GetMapping("deployments/functions/{functionId}/versions/{functionVersionId}")
    @Operation(
            summary = "Get Function Deployment Details",
            description = "Retrieves deployment details of the specified function."
                    + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public DeploymentResponse getFunctionDeployment(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @NotNull @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @NotNull @PathVariable UUID functionVersionId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.getFunctionDeployment(ncaId,
                                                              functionId,
                                                              functionVersionId,
                                                              authentication);
    }

    @GetMapping("deployments/{deploymentId}")
    @Operation(
            summary = "Get Function Deployment Details",
            description = "Retrieves deployment details of the specified deployment id."
                    + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public DeploymentResponse getFunctionDeployment(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Deployment id", required = true)
            @NotNull @PathVariable UUID deploymentId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.getFunctionDeployment(ncaId,
                                                              deploymentId,
                                                              authentication);
    }

    @GetMapping("deployments")
    @Operation(
            summary = "Get All Deployments",
            description = "Retrieves deployments details of all the functions in the specified " +
                    "account." + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public ListDeploymentsResponse getAllFunctionDeployments(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.getAllFunctionDeployments(ncaId, authentication);
    }

    @PutMapping(value = "deployments/functions/{functionId}/versions/{functionVersionId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update Function Deployment",
            description = """
                    Deprecated and will be removed soon. Use a single GPU specification update
                     method instead.
                    
                    Updates the deployment specs of the specified function version. It's important
                     to note that GPU type and backend configurations cannot be modified through
                     this endpoint.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    @Deprecated
    public DeploymentResponse updateFunctionDeployment(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Function id", required = true)
            @PathVariable UUID functionId,
            @Parameter(description = "Function version id", required = true)
            @PathVariable UUID functionVersionId,
            @NotNull @Valid @RequestBody UpdateFunctionDeploymentRequest updateDeploymentRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        return functionDeploymentFacade.updateFunctionDeployment(ncaId,
                                                                 functionId,
                                                                 functionVersionId,
                                                                 updateDeploymentRequest,
                                                                 httpServletRequest,
                                                                 authentication);
    }

    @PatchMapping(value = "deployments/{deploymentId}/gpu-specifications/{gpuSpecId}",
            consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Update single GPU specification under Function Deployment",
            description = """
                     Updates the gpu specification of the specified function version's deployment.
                     It's important to note that GPU type and backend configurations cannot be
                     modified through this endpoint. If the specified function is public,
                     then Account Admin cannot perform this operation.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:deploy_function')")
    public UpdateGpuSpecificationResponse updateGpuSpecification(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId,
            @Parameter(description = "Deployment id", required = true)
            @NotNull @PathVariable UUID deploymentId,
            @Parameter(description = "GPU Specification id", required = true)
            @NotNull @PathVariable UUID gpuSpecId,
            @NotNull @Valid @RequestBody UpdateGpuSpecificationRequest updateGpuSpecRequest,
            HttpServletRequest httpServletRequest,
            Authentication authentication) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        accountService.assertAccountExistsOrThrow(ncaId);
        accountService.assertAccountIdFromPathMatches(ncaId, authentication);
        rateLimiterService.verifyLimits(ncaId);

        var deploymentEntity =
                functionDeploymentLookupService.getFunctionDeploymentEntityOrThrow(deploymentId);
        return functionDeploymentFacade.updateGpuSpecification(deploymentEntity,
                                                               gpuSpecId,
                                                               updateGpuSpecRequest,
                                                               httpServletRequest,
                                                               authentication);
    }
}
