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

import static org.junit.jupiter.api.Assertions.assertThrows;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.boot.registries.service.registry.dto.ArtifactTypeEnum;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;

class NgcArtifactUriValidatorTest {

    // ==================== Valid Model Strings ====================

    static Stream<String> validModelUris() {
        return Stream.of(
                // Production URLs
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                // Staging URLs
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                // Canary URLs
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                // Various org/team name formats
                "https://api.ngc.nvidia.com/v2/org/org-with-dash/models/model-name/v1.2.3/files",
                "https://api.ngc.nvidia.com/v2/org/org_underscore/team/team_name/models/my_model/1.0/files");
    }

    // ==================== Invalid Model Strings ====================

    static Stream<String> invalidModelUris() {
        return Stream.of(
                // Invalid path format
                "https://api.stg.ngc.nvidia.com/some-file-location/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team//models/playground_llama2_trt_l40g/0.1/files",
                "https://api.stg.ngc.nvidia.com/v3/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                // String contains "versions" (extra info path)
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/versions/0.1/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/versions/0.1/files",
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/versions/0.1/files",
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/versions/0.1/files",
                // Missing or invalid protocol prefix
                "api.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                "api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                // Invalid subdomain
                "https://api.abc.ngc.nvidia.com/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                // Non-fully qualified URLs (path only) - not supported
                "/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                "/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/0.1/files",
                "/v2/org/whw3rcpsilnj/team//models/playground_llama2_trt_l40g/0.1/files",
                "/v3/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/0.1/files",
                "/v2/org/whw3rcpsilnj/models/playground_llama2_trt_l40g/versions/0.1/files",
                "/v2/org/whw3rcpsilnj/team/jeff/models/playground_llama2_trt_l40g/versions/0.1/files",
                "/some-file-location/files",
                "https://api.ngc.nvidia.com/v2/org/org_underscore/team/team_name/models/my_model/1.0/files?anyQueryParams=123");
    }

    // ==================== Valid Resource Strings ====================

    static Stream<String> validResourceUris() {
        return Stream.of(
                // Production URLs
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files",
                // Staging URLs
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files",
                // Canary URLs
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files");
    }

    // ==================== Invalid Resource Strings ====================

    static Stream<String> invalidResourceUris() {
        return Stream.of(
                // Invalid path format
                "https://api.stg.ngc.nvidia.com/some-file-location/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team//resources/my_resource/0.1/files",
                "https://api.stg.ngc.nvidia.com/v3/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                // String contains "versions" (extra info path)
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/versions/0.1/files",
                "https://api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/versions/0.1/files",
                "https://api.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/versions/0.1/files",
                "https://api.canary.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/versions/0.1/files",
                // Missing or invalid protocol prefix
                "api.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files",
                "api.stg.ngc.nvidia.com/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files",
                // Invalid subdomain
                "https://api.abc.ngc.nvidia.com/v2/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                // Non-fully qualified URLs (path only) - not supported
                "/v2/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                "/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/0.1/files",
                "/v2/org/whw3rcpsilnj/team//resources/my_resource/0.1/files",
                "/v3/org/whw3rcpsilnj/resources/my_resource/0.1/files",
                "/v2/org/whw3rcpsilnj/resources/my_resource/versions/0.1/files",
                "/v2/org/whw3rcpsilnj/team/jeff/resources/my_resource/versions/0.1/files",
                "/some-file-location/files");
    }

    // ==================== Single String Validation Tests ====================

    @ParameterizedTest
    @MethodSource("validModelUris")
    void isValid_shouldAcceptValidModelUri(String uri) {
        NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.MODEL);
    }

    @ParameterizedTest
    @MethodSource("invalidModelUris")
    void isValid_shouldRejectInvalidModelUri(String uri) {
        assertThrows(BadRequestException.class,
                     () -> NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.MODEL));
    }

    @ParameterizedTest
    @MethodSource("validResourceUris")
    void isValid_shouldAcceptValidResourceUri(String uri) {
        NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.RESOURCE);
    }

    @ParameterizedTest
    @MethodSource("invalidResourceUris")
    void isValid_shouldRejectInvalidResourceUri(String uri) {
        assertThrows(BadRequestException.class,
                     () -> NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.RESOURCE));
    }

    @Test
    void isValid_shouldRejectNullUri() {
        assertThrows(BadRequestException.class,
                     () -> NgcArtifactUriValidator.validate(null, ArtifactTypeEnum.MODEL));
        assertThrows(BadRequestException.class,
                     () -> NgcArtifactUriValidator.validate(null,
                                                            ArtifactTypeEnum.RESOURCE));
    }


    // ==================== Unsupported Artifact Type Tests ====================

    @Test
    void isValid_shouldThrowExceptionForContainerType() {
        var uri = "https://api.ngc.nvidia.com/v2/org/myorg/models/mymodel/1.0/files";
        assertThrows(IllegalArgumentException.class,
                     () -> NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.CONTAINER));
    }

    @Test
    void isValid_shouldThrowExceptionForHelmType() {
        var uri = "https://api.ngc.nvidia.com/v2/org/myorg/models/mymodel/1.0/files";
        assertThrows(IllegalArgumentException.class,
                     () -> NgcArtifactUriValidator.validate(uri, ArtifactTypeEnum.HELM));
    }
}
