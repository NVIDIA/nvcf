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
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.tags.Tag;
import java.util.Map;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Deprecated(forRemoval = true)
@Tag(name = "Cluster Groups and GPUs",
        description = """
                Deprecated. Please use corresponding ICMS endpoints directly. These endpoints
                 will be removed.

                Defines endpoints to list Cluster Groups and GPUs for Account Admins. All tne
                 endpoints defined in this API require a bearer token with 'list_cluster_groups'
                 scope in the HTTP Authorization header.""")
public class ClusterGroupController {
    private static final String AUTH_DESCRIPTION = """
           Requires a bearer token with 'list_cluster_groups' scope in HTTP Authorization header.
            """;

    private final AccountService accountService;
    private final ClusterGroupFacade clusterGroupFacade;
    private final Tracer tracer;

    @GetMapping("/v2/nvcf/clusterGroups")
    @Operation(
            summary = "List Cluster Groups",
            description = """
                    Lists Cluster Groups for the current account. The response includes cluster
                     groups defined specifically in the current account and publicly available
                     cluster groups such as GFN, OCI, etc.
                    """ + AUTH_DESCRIPTION
    )
    @PreAuthorize("hasAuthority('list_cluster_groups')")
    public ClusterGroupsResponse getClusterGroups(
            @NonNull Authentication authentication) {
        var ncaId =  accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return clusterGroupFacade.getClusterGroups(ncaId, InstanceUsageTypeEnum.DEFAULT);
    }

}
