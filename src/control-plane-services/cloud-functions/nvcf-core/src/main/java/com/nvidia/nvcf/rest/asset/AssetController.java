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
package com.nvidia.nvcf.rest.asset;

import static com.nvidia.nvcf.util.NvcfConstants.SPAN_TAG_NCA_ID;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.nvcf.rest.asset.dto.AssetRedirectResponse;
import com.nvidia.nvcf.rest.asset.dto.AssetResponse;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetRequest;
import com.nvidia.nvcf.rest.asset.dto.CreateAssetResponse;
import com.nvidia.nvcf.rest.asset.dto.ListAssetsResponse;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.util.NvcfUtils;
import io.micrometer.tracing.Tracer;
import io.swagger.v3.oas.annotations.Hidden;
import io.swagger.v3.oas.annotations.Operation;
import io.swagger.v3.oas.annotations.Parameter;
import io.swagger.v3.oas.annotations.responses.ApiResponse;
import io.swagger.v3.oas.annotations.tags.Tag;
import jakarta.validation.Valid;
import java.util.Map;
import java.util.UUID;
import lombok.NonNull;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.http.HttpStatus;
import org.springframework.security.access.prepost.PreAuthorize;
import org.springframework.security.core.Authentication;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

@Slf4j
@RestController
@RequiredArgsConstructor
@RequestMapping(produces = APPLICATION_JSON_VALUE)
@Deprecated(forRemoval = true)
@Tag(name = "Asset Management",
        description = """
                Deprecated. Users can provide assets directly as part of the input during
                 function invocation. There is no need to upload assets and then include the
                 asset-id as part of input to the function invocation.

                Defines Asset Management endpoints for Account Admins/Users. All the endpoints
                 defined in this API require a bearer token in the HTTP Authorization header with
                 'invoke_function' scope."""
)
public class AssetController {

    private static final String MESG_FAILED_TO_CREATE_PRE_SIGNED_URL =
            "Failed to create pre-signed upload URL for asset: '{}'";

    private final AssetManagementFacade assetManagementFacade;
    private final AccountService accountService;
    private final Tracer tracer;

    @PostMapping(value = "/v2/nvcf/assets", consumes = APPLICATION_JSON_VALUE)
    @Operation(
            summary = "Create Asset",
            description = """
                    Creates a unique id representing an asset and a pre-signed URL to upload the
                     asset artifact to AWS S3 bucket for the NVIDIA Cloud Account. Requires a
                     bearer token  with 'invoke_function' scope in the HTTP Authorization header.
                     """
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public CreateAssetResponse createAsset(
            @Valid @NonNull @RequestBody CreateAssetRequest assetRequest,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        try {
            return assetManagementFacade.createAsset(assetRequest, ncaId);
        } catch (Exception ex) {
            log.error(MESG_FAILED_TO_CREATE_PRE_SIGNED_URL, ex.getMessage());
            throw ex;
        }
    }

    @GetMapping("/v2/nvcf/assets")
    @Operation(
            summary = "List Assets",
            description = """
                    List assets owned by the current NVIDIA Cloud Account. Requires a
                     bearer token with 'invoke_function' scope in the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public ListAssetsResponse getAssets(Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return assetManagementFacade.getAssets(ncaId);
    }

    @GetMapping("/v2/nvcf/assets/{assetId}")
    @Operation(
            summary = "Show Asset Details",
            description = """
                    Returns details for the specified asset-id belonging to the current NVIDIA
                     Cloud Account. Requires a bearer token with 'invoke_function' scope in
                     the HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public AssetResponse getAsset(
            @Parameter(description = "Asset id", required = true)
            @NonNull @PathVariable("assetId") UUID assetId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return assetManagementFacade.getAsset(ncaId, assetId);
    }

    @Hidden
    @GetMapping("/v2/nvcf/assets/{assetId}/content-redirect")
    @Operation(
            summary = "Download Asset",
            description = """
                    Download asset with the specified asset-id belonging to the current NVIDIA
                     Cloud Account. Requires a bearer token with 'invoke_function' scope in the
                     HTTP Authorization header.
                    """
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    public AssetRedirectResponse getAssetRedirect(
            @Parameter(description = "Asset id", required = true)
            @NonNull @PathVariable("assetId") UUID assetId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        return assetManagementFacade.downloadAsset(ncaId, assetId);
    }

    @DeleteMapping("/v2/nvcf/assets/{assetId}")
    @Operation(
            summary = "Delete Asset",
            description = """
                    Deletes asset belonging to the current NVIDIA Cloud Account. Requires
                     a bearer token with 'invoke_function' scope in the HTTP Authorization header.
                     """,
            responses = @ApiResponse(responseCode = "204")
    )
    @PreAuthorize("hasAnyAuthority('invoke_function', 'apikey:invoke_function')")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void deleteAsset(
            @Parameter(description = "Id of the asset to be deleted", required = true)
            @NonNull @PathVariable("assetId") UUID assetId,
            Authentication authentication) {
        var ncaId = accountService.getNcaId(authentication);
        NvcfUtils.addTagsToCurrentSpan(tracer, Map.of(SPAN_TAG_NCA_ID, ncaId));
        assetManagementFacade.deleteAsset(ncaId, assetId);
    }

}
