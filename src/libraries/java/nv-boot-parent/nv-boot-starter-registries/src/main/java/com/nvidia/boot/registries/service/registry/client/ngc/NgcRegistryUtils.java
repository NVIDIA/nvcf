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

package com.nvidia.boot.registries.service.registry.client.ngc;

import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_MODELS_VERSIONS_PATH;
import static com.nvidia.boot.registries.util.RegistriesConstants.CAS_RESOURCES_VERSIONS_PATH;

import java.net.URI;
import java.util.Base64;
import lombok.experimental.UtilityClass;

@UtilityClass
public class NgcRegistryUtils {

    public static final String MESG_FETCHED_SIZE_ARTIFACT_URLS =
            "Fetched size of artifact urls for '{}': {}";
    public static final String MESG_FETCHED_PRESIGNED_ARTIFACT_URLS =
            "Fetched pre-signed artifact urls for '{}': {}";
    private static final String MESG_UNEXPECTED_SECRET_FORMAT =
            "Unexpected format secret get. It should be 'username:password' with base64 encoded ";

    // To get artifact file size for allocate instance cache and other usage, we need to call
    // the endpoint to get artifact metadata. The function can help convert user provide artifact
    // file url to metadata endpoint which providing artifact size info.
    // The following artifact file url endpoints
    //    `/v2/org/{orgName}/models/{modelName}/{modelVersion}/files`
    //    `/v2/org/{orgName}/resources/{resourceName}/{resourceVersion}/files`
    // will be translated to
    //    `/v2/org/{orgName}/models/{modelName}/versions/{modelVersion}`
    //    `/v2/org/{orgName}/resources/{resourceName}/versions/{resourceVersion}`
    public static String convertArtifactFileUrlToSizeUrl(String path) {
        var match = CAS_MODELS_VERSIONS_PATH.matcher(path).matches();

        int index = path.lastIndexOf("/files");
        if (match) {
            return path.substring(0, index);
        }
        match = CAS_RESOURCES_VERSIONS_PATH.matcher(path).matches();
        if (match) {
            return path.substring(0, index);
        }

        var versionPath = path.substring(0, index);
        index = versionPath.lastIndexOf('/');
        return versionPath.substring(0, index) +
                "/versions/" +
                versionPath.substring(index + 1);
    }

    public static String removeArtifactHostName(String artifactPath) {
        var newArtifactPath = artifactPath;
        if (!artifactPath.startsWith("http")) {
            newArtifactPath = "https://" + artifactPath;
        }
        var uri = URI.create(newArtifactPath);
        return uri.getPath();
    }

    public static String getApiKey(String secret) {
        var secretText = new String(Base64.getDecoder().decode(secret));
        if (!secretText.contains(":") || secretText.indexOf(":") == secretText.length() - 1) {
            throw new IllegalArgumentException(MESG_UNEXPECTED_SECRET_FORMAT);
        }
        return secretText.substring(secretText.indexOf(":") + 1);
    }
}
