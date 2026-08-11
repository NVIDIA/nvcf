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
package com.nvidia.nvcf.rest.function.management.dto;

import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_DESCRIPTION_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAGS_COUNT;
import static com.nvidia.nvcf.util.NvcfConstants.MAX_TAG_LENGTH;
import static com.nvidia.nvcf.util.NvcfConstants.TAG_REGEX;

import com.google.common.annotations.VisibleForTesting;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.rest.telemetry.dto.TelemetriesDto;
import io.swagger.v3.oas.annotations.media.Schema;
import jakarta.annotation.Nullable;
import jakarta.validation.Valid;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import java.net.URI;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import lombok.AccessLevel;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import lombok.NonNull;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.hibernate.validator.constraints.Length;
import org.springframework.util.CollectionUtils;
import tools.jackson.databind.annotation.JsonDeserialize;
import tools.jackson.databind.util.StdConverter;

@Valid
@Builder
@Data
@NoArgsConstructor
// Using AcessLevel.PRIVATE to keep the builder working and to steer Jackson 3 towards no-args
// constructor plus setters during serialization. If the all-args constructor is public,
// Jackson 3 will use it during serialization and pass null values for fields that are not
// specified in the payload. As a result, the default values specified for fields such as
// functionType, etc. get wiped out. By making the all-args constructor private, we keep the
// builder working and the default values don't get wiped out when the payload doesn't include
// those fields.
@AllArgsConstructor(access = AccessLevel.PRIVATE)
@Schema(description = "Request payload to create function.")
@JsonDeserialize(converter = CreateFunctionRequest.CreateFunctionRequestValidator.class)
public class CreateFunctionRequest {

    private static final String FUNCTION_NAME_REGEX = "^[a-z0-9A-Z][a-z0-9A-Z\\-_]*$";
    public static final String GO = "GO";

    @Schema(description = "Function name must start with lowercase/uppercase/digit and can " +
            "only contain lowercase, uppercase, digit, hyphen, and underscore characters")
    @NonNull
    @NotNull
    @Pattern(regexp = FUNCTION_NAME_REGEX,
            message = "Invalid function name: Must conform to regex " + FUNCTION_NAME_REGEX)
    @Size(min = 1, max = 128, message = "Invalid function name: must be 1 - 128 characters long")
    private String name;

    @NonNull
    @Schema(description = "Entrypoint for invoking the container to process a request")
    @NotNull
    private URI inferenceUrl;

    /**
     * @deprecated Use HealthDto
     */
    @Nullable
    @Schema(description = "Health endpoint for the container or the helmChart")
    @Deprecated
    private URI healthUri;

    @Nullable
    @Schema(description = """
            Optional port number where the inference listener is running. Defaults to 8000
             for Triton.
            """)
    private Integer inferencePort;

    @Nullable
    @Schema(description = "Function health")
    @Valid
    private HealthDto health;

    @Nullable
    @Schema(description = "Args to be passed when launching the container")
    private String containerArgs;

    @Nullable
    @Schema(description = "Environment settings for launching the container")
    @Valid
    private List<ContainerEnvironmentEntryDto> containerEnvironment;

    @Nullable
    @Schema(description = "Optional list of models")
    @Valid
    private List<FunctionModelDto> models;

    @Nullable
    @Schema(description = "Optional custom container image")
    private URI containerImage;

    @Nullable
    @Schema(description = """
            Optional field that serves both as a selector for the configured Utils Container
             image URIs as well as custom Utils Container URI to be used by the Worker.
             Valid values are PYTHON, GO, and raw URI. Defaults to PYTHON. If the specified
             value does not match PYTHON or GO, it is considered to be raw URI of custom Utils
             Container image and is passed as-is to the Worker.
            """,
            hidden = true)
    private String utilsContainerImage;

    @Nullable
    @Schema(description = "Optional Helm Chart")
    private URI helmChart;

    @Nullable
    @Schema(description = """
            Helm Chart Service Name is required when helmChart property is specified
            """)
    private String helmChartServiceName;

    @Nullable
    @Schema(description = "Optional set of resources")
    @Valid
    private Set<ArtifactDto> resources;

    @Builder.Default
    @Nullable
    @Schema(description = "Invocation request body format")
    private ApiBodyFormatEnum apiBodyFormat = ApiBodyFormatEnum.CUSTOM;

    @Nullable
    @Schema(description = "Optional set of tags - could be empty. Provided by user")
    @Valid
    @Size(max = MAX_TAGS_COUNT, message = "Maximum number of tags of " + MAX_TAGS_COUNT +
            " is exceeded.")
    private Set<@Length(max = MAX_TAG_LENGTH, message = "Maximum tag length of " + MAX_TAG_LENGTH +
            " is exceeded.")
    @Pattern(regexp = TAG_REGEX)
            String> tags;

    @Nullable
    @Schema(description = "Optional function/version description")
    @Length(max = MAX_DESCRIPTION_LENGTH,
            message = "Maximum description length of " + MAX_DESCRIPTION_LENGTH + " is exceeded.")
    private String description;

    @Nullable
    @Schema(description = "Optional secrets")
    @Valid
    private Set<SecretDto> secrets;

    @Builder.Default
    @Nullable
    @Schema(description = "Optional function type, used to indicate a STREAMING function. Defaults to DEFAULT.")
    private FunctionTypeEnum functionType = FunctionTypeEnum.DEFAULT;

    @Nullable
    @Schema(description = "Optional rate limit config")
    @Valid
    private RateLimitDto rateLimit;

    @Nullable
    @Schema(description = "Optional function-level LLM invocation configuration. Only valid for "
            + "LLM-type functions.")
    @Valid
    private LlmInvocationConfigDto llmInvocationConfig;

    @Nullable
    @Schema(description = "Optional telemetry configuration for logs, metrics, and traces.")
    @Valid
    private TelemetriesDto telemetries;


    @Slf4j
    static class CreateFunctionRequestValidator extends
            StdConverter<CreateFunctionRequest, CreateFunctionRequest> {

        private static final String PROP_CONTAINER_IMAGE = "containerImage";
        private static final String PROP_CONTAINER_ARGS = "containerArgs";
        private static final String PROP_CONTAINER_ENVIRONMENT = "containerEnvironment";
        private static final String PROP_HELM_CHART = "helmChart";
        private static final String MESG_MISSING_REQ_PROPS =
                "Invalid request: One of the following properties 'containerImage', 'models', " +
                        "or 'helmChart' must be specified in the payload";
        private static final String MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION =
                "Invalid request: Cannot specify both '%s' and '%s' properties in the payload";

        private static final String MESG_REQ_PROPS_CONSTRAINT_VIOLATION =
                "Invalid request: Either both or none of 'helmChart' and 'helmChartServiceName' "
                        + "properties must be specified";
        private static final String MESG_MISSING_FUNCTION_NAME =
                "Invalid request: 'name' cannot be empty/null in the create function payload";
        private static final String MESG_EMPTY_HEALTH_URI = """
                Invalid request: Health URI cannot be empty/null in the create function payload
                """;
        private static final String MESG_INVALID_TELEMETRY_REQUEST =
                "Invalid request: Telemetry object must have at least one UUID specified.";
        private static final String MESG_MISSING_LLM_MODELS =
                "Invalid request: 'models' must be specified when functionType is LLM";
        private static final String MESG_MISSING_LLM_MODEL_CONFIG =
                "Invalid request: LLM function models must specify 'llmConfig'";
        private static final String MESG_MISSING_LLM_MODEL_URIS =
                "Invalid request: LLM function models must specify non-empty 'llmConfig.uris'";
        private static final String MESG_DUPLICATE_MODEL_NAMES =
                "Invalid request: duplicate model names are not allowed in 'models'";
        private static final String MESG_NON_LLM_MODEL_VERSION_URI_REQUIRED =
                "Invalid request: non-LLM function models must specify both 'version' and 'uri'";

        @Override
        public CreateFunctionRequest convert(CreateFunctionRequest value) {
            if (StringUtils.isBlank(value.getName())) {
                log.error(MESG_MISSING_FUNCTION_NAME);
                throw new BadRequestException(MESG_MISSING_FUNCTION_NAME);
            }

            URI helmChart = value.getHelmChart();
            String helmChartServiceName = value.getHelmChartServiceName();
            if ((value.getContainerImage() == null)
                    && CollectionUtils.isEmpty(value.getModels())
                    && (helmChart == null)) {
                log.error(MESG_MISSING_REQ_PROPS);
                throw new BadRequestException(MESG_MISSING_REQ_PROPS);
            }

            if (helmChart != null && value.getContainerImage() != null) {
                var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                        .formatted(PROP_CONTAINER_IMAGE, PROP_HELM_CHART);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            if (helmChart != null && StringUtils.isNotBlank(value.getContainerArgs())) {
                var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                        .formatted(PROP_CONTAINER_ARGS, PROP_HELM_CHART);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            if (helmChart != null && value.getContainerEnvironment() != null) {
                var mesg = MESG_ONE_OF_PROPS_CONSTRAINT_VIOLATION
                        .formatted(PROP_CONTAINER_ENVIRONMENT, PROP_HELM_CHART);
                log.error(mesg);
                throw new BadRequestException(mesg);
            }

            if ((helmChart != null && StringUtils.isBlank(helmChartServiceName)) ||
                    (helmChart == null && StringUtils.isNotBlank(helmChartServiceName))) {
                log.error(MESG_REQ_PROPS_CONSTRAINT_VIOLATION);
                throw new BadRequestException(MESG_REQ_PROPS_CONSTRAINT_VIOLATION);
            }

            if (StringUtils.isBlank(value.getUtilsContainerImage())) {
                value.setUtilsContainerImage(GO);
            }

            if (value.getModels() != null) {
                validateUniqueModelNames(value.getModels());
                validateModelFields(value.getModels(), value.getFunctionType());

                // For Model-only functions, if containerArgs is blank, then set the
                // default value.
                if (StringUtils.isBlank(value.getContainerArgs())
                        && (value.getContainerImage() == null)
                        && (helmChart == null)) {
                    value.setContainerArgs(DEFAULT_CONTAINER_ARGS_FOR_MODEL_ONLY_FUNCTIONS);
                }
            }

            if (value.getHealth() != null
                    && StringUtils.isBlank(value.getHealth().getUri().toString())) {
                throw new BadRequestException(MESG_EMPTY_HEALTH_URI);
            }

            if (value.getTelemetries() != null) {
                validateTelemetry(value.getTelemetries());
            }

            if (FunctionTypeEnum.LLM.equals(value.getFunctionType())
                    && CollectionUtils.isEmpty(value.getModels())) {
                log.error(MESG_MISSING_LLM_MODELS);
                throw new BadRequestException(MESG_MISSING_LLM_MODELS);
            }

            return value;
        }

        @VisibleForTesting
        static void validateTelemetry(TelemetriesDto telemetry) {
            if (telemetry.logsTelemetryId() == null &&
                    telemetry.metricsTelemetryId() == null &&
                    telemetry.tracesTelemetryId() == null) {
                log.error(MESG_INVALID_TELEMETRY_REQUEST);
                throw new BadRequestException(MESG_INVALID_TELEMETRY_REQUEST);
            }
        }

        @VisibleForTesting
        static void validateUniqueModelNames(List<FunctionModelDto> models) {
            var uniqueNames = new HashSet<String>();
            for (var model : models) {
                if (!uniqueNames.add(model.getName())) {
                    log.error(MESG_DUPLICATE_MODEL_NAMES);
                    throw new BadRequestException(MESG_DUPLICATE_MODEL_NAMES);
                }
            }
        }

        @VisibleForTesting
        static void validateModelFields(List<FunctionModelDto> models,
                                        @Nullable FunctionTypeEnum functionType) {
            var isLlmFunction = FunctionTypeEnum.LLM.equals(functionType);
            for (var model : models) {
                if (isLlmFunction && model.getLlmConfig() == null) {
                    log.error(MESG_MISSING_LLM_MODEL_CONFIG);
                    throw new BadRequestException(MESG_MISSING_LLM_MODEL_CONFIG);
                }
                if (isLlmFunction && CollectionUtils.isEmpty(model.getLlmConfig().getUris())) {
                    log.error(MESG_MISSING_LLM_MODEL_URIS);
                    throw new BadRequestException(MESG_MISSING_LLM_MODEL_URIS);
                }
                if (isLlmFunction) {
                    LlmConfigValidator.validateRoutingMethod(
                            model.getName(), model.getLlmConfig().getRoutingMethod());
                    LlmConfigValidator.validateTokenRateLimit(
                            model.getName(), model.getLlmConfig().getTokenRateLimit());
                }
                if (!isLlmFunction && (model.getVersion() == null || model.getUri() == null)) {
                    log.error(MESG_NON_LLM_MODEL_VERSION_URI_REQUIRED);
                    throw new BadRequestException(MESG_NON_LLM_MODEL_VERSION_URI_REQUIRED);
                }
            }
        }
    }
}
