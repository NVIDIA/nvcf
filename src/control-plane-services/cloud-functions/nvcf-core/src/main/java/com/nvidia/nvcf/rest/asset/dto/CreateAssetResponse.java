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
package com.nvidia.nvcf.rest.asset.dto;

import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import java.net.URL;
import java.util.UUID;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

@Builder
@Data
@NoArgsConstructor
@AllArgsConstructor
@Schema(description = "Response body containing asset-id and the corresponding " +
        "pre-signed URL to upload an asset of specified content-type to AWS S3 bucket.")
public class CreateAssetResponse {

    @Schema(description = "Unique id of the asset to be uploaded to AWS S3 bucket")
    @NotNull
    private UUID assetId;

    @Schema(description = "Pre-signed upload URL to upload asset")
    @NotNull
    private URL uploadUrl;

    @Schema(description = "Content type of the asset such image/png, image/jpeg, etc.")
    @NotBlank
    private String contentType;

    @Schema(description = "Asset description to be used when uploading the asset")
    @NotBlank
    private String description;

}
