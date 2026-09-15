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
package com.nvidia.icms.service.telemetry.model;

import lombok.Data;

@Data
public class UnifiedErrorMetric {
    private String eventName;

    private Double eventTimestamp;
    private String env;
    private String region;
    private String podName;

    private String requestId;
    private String instanceId;

    private String functionId;
    private String functionVersionId;
    private String taskId;

    private String deploymentId;
    private String gpuSpecificationId;
    private String ncaId;

    private int httpCode;

    private String errorName;
    private String errorHexCode;
    private String errorMessage;

    private String errorOriginService;
    private String errorReportingService;
    private String errorSeverity;
    private String errorServiceInternalCode;
    private String errorDescription;


    public UnifiedErrorMetric withEventName(String eventName) {
        this.eventName = eventName;
        return this;
    }

    public UnifiedErrorMetric withEventTimestamp(Double eventTimestamp) {
        this.eventTimestamp = eventTimestamp;
        return this;
    }

    public UnifiedErrorMetric withEnv(String env) {
        this.env = env;
        return this;
    }

    public UnifiedErrorMetric withRegion(String region) {
        this.region = region;
        return this;
    }

    public UnifiedErrorMetric withRequestId(String requestId) {
        this.requestId = requestId;
        return this;
    }

    public UnifiedErrorMetric withInstanceId(String instanceId) {
        this.instanceId = instanceId;
        return this;
    }

    public UnifiedErrorMetric withFunctionId(String functionId) {
        this.functionId = functionId;
        return this;
    }

    public UnifiedErrorMetric withFunctionVersionId(String functionVersionId) {
        this.functionVersionId = functionVersionId;
        return this;
    }

    public UnifiedErrorMetric withTaskId(String taskId) {
        this.taskId = taskId;
        return this;
    }

    public UnifiedErrorMetric withDeploymentId(String deploymentId) {
        this.deploymentId = deploymentId;
        return this;
    }

    public UnifiedErrorMetric withGpuSpecificationId(String gpuSpecificationId) {
        this.gpuSpecificationId = gpuSpecificationId;
        return this;
    }

    public UnifiedErrorMetric withNcaId(String ncaId) {
        this.ncaId = ncaId;
        return this;
    }

    public UnifiedErrorMetric withHttpCode(int httpCode) {
        this.httpCode = httpCode;
        return this;
    }

    public UnifiedErrorMetric withErrorName(String errorName) {
        this.errorName = errorName;
        return this;
    }

    public UnifiedErrorMetric withErrorHexCode(String errorHexCode) {
        this.errorHexCode = errorHexCode;
        return this;
    }

    public UnifiedErrorMetric withErrorMessage(String errorMessage) {
        this.errorMessage = errorMessage;
        return this;
    }

    public UnifiedErrorMetric withErrorOriginService(String errorOriginService) {
        this.errorOriginService = errorOriginService;
        return this;
    }

    public UnifiedErrorMetric withErrorReportingService(String errorReportingService) {
        this.errorReportingService = errorReportingService;
        return this;
    }

    public UnifiedErrorMetric withErrorSeverity(String errorSeverity) {
        this.errorSeverity = errorSeverity;
        return this;
    }

    public UnifiedErrorMetric withErrorServiceInternalCode(String errorServiceInternalCode) {
        this.errorServiceInternalCode = errorServiceInternalCode;
        return this;
    }

    public UnifiedErrorMetric withErrorDescription(String errorDescription) {
        this.errorDescription = errorDescription;
        return this;
    }


}
