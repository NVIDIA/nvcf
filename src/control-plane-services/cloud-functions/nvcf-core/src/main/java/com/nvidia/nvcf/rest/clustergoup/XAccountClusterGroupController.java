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
package com.nvidia.nvcf.rest.clustergoup;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.icms.client.IcmsStubService.ClusterGroupsResponse;
import com.nvidia.nvcf.rest.function.deployment.dto.InstanceUsageTypeEnum;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.tags.Tag;
import java.util.Map;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Deprecated(forRemoval = true)
@Tag(name = "Cross-Account Cluster Groups and GPUs for NVIDIA Super Admins",
        description = """
                Deprecated. Please use corresponding ICMS endpoints directly. These endpoints
                 will be removed.

                Defines endpoints to list Cluster Groups and GPUs across NVIDIA Cloud Accounts
                 for NVIDIA Super Admins. All tne endpoints defined in this API require a bearer
                 token with 'admin:list_cluster_groups' scope in the HTTP Authorization header.""")
public class XAccountClusterGroupController {

    private static final String AUTH_DESCRIPTION = """
            Requires a bearer token with 'admin:list_cluster_groups' scope in HTTP Authorization
             header.
            """;
    private static final String NCA_ID_DESCRIPTION = "NVIDIA Cloud Account Id";

    private final ClusterGroupFacade clusterGroupFacade;
    private final AccountService accountService;
    private final RateLimiterService rateLimiterService;
    private final Tracer tracer;

    @GetMapping("/v2/nvcf/accounts/{ncaId}/clusterGroups")
    @Operation(
            summary = "List Cluster Groups",
            description = """
                    Lists Cluster Groups for the specified account. The response includes cluster
                     groups defined in the specified account and publicly available cluster groups
                     such as GFN, OCI, etc.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('admin:list_cluster_groups')")
    public ClusterGroupsResponse getClusterGroups(
            @Parameter(description = NCA_ID_DESCRIPTION, required = true)
            @PathVariable String ncaId) {
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        rateLimiterService.verifyLimits(ncaId);
        accountService.assertAccountExistsOrThrow(ncaId);
        return clusterGroupFacade.getClusterGroups(ncaId, InstanceUsageTypeEnum.DEFAULT);
    }
}
