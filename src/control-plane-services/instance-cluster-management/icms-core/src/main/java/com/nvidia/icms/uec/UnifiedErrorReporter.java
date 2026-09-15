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
package com.nvidia.icms.uec;

import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.UnifiedErrorMetric;
import com.nvidia.icms.uec.internal.v2.DescriptiveUnifiedError;
import jakarta.validation.constraints.NotNull;
import java.util.List;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.http.HttpStatus;
import org.springframework.stereotype.Component;

@Slf4j
@AllArgsConstructor
@Component
public class UnifiedErrorReporter {

    final TelemetryEventClient telemetryEventClient;

    public void reportAndThrow(@NotNull IcmsHttpUnifiedErrorException exception) throws IcmsHttpUnifiedErrorException {
        logError(exception.unifiedError(), exception.getHttpStatus(), exception.getMessage(),
                 exception.unifiedErrorData(), exception);

        sendErrorMetric(exception.unifiedError(), exception.getHttpStatus(), exception.getMessage(),
                        exception.unifiedErrorData());
        throw exception;
    }

    public void reportAndThrow(@NotNull UnifiedErrorException exception) throws UnifiedErrorException {
        logError(exception.unifiedError(), null, exception.getMessage(),
                 exception.unifiedErrorData(), exception);

        sendErrorMetric(exception.unifiedError(), null, exception.getMessage(),
                        exception.unifiedErrorData());
        throw exception;
    }

    private void logError(
            @NotNull DescriptiveUnifiedError icmsUnifiedError, @Nullable HttpStatus httpStatus,
            @Nullable String message, @Nullable UnifiedErrorData unifiedErrorData,
            @Nullable Throwable cause) {
        String logMessage;
        String errorDataLogMessage = unifiedErrorData != null ? unifiedErrorData.toString() : "N/A";

        if (httpStatus == null) {
            logMessage = String.format("UnifiedError: %s (%s): %s. ErrorData: %s. ErrorInfo: %s",
                                       icmsUnifiedError.errorName(),
                                       icmsUnifiedError.hexCode(),
                                       message,
                                       errorDataLogMessage,
                                       getMessageForUnifiedError(icmsUnifiedError));

        } else {
            logMessage = String.format(
                    "Unified Error %s (%s), Http Status: %s: %s. ErrorData: %s. ErrorInfo: %s",
                    icmsUnifiedError.errorName(),
                    icmsUnifiedError.errorCode(),
                    httpStatus,
                    message,
                    errorDataLogMessage,
                    getMessageForUnifiedError(icmsUnifiedError));
        }

        if (icmsUnifiedError.severity().isError()) {
            log.error(logMessage, cause);
        } else {
            log.warn(logMessage, cause);
        }
    }

    private void sendErrorMetric(
            @NotNull DescriptiveUnifiedError icmsUnifiedError, @Nullable HttpStatus httpStatus,
            @Nullable String message, @Nullable UnifiedErrorData unifiedErrorData) {
        UnifiedErrorMetric unifiedErrorMetric = new UnifiedErrorMetric()
                .withEventName(Events.UNIFIED_ERROR.toString())
                .withErrorName(icmsUnifiedError.errorName())
                .withErrorHexCode(icmsUnifiedError.hexCode())
                .withErrorMessage(message)
                .withHttpCode(httpStatus != null ? httpStatus.value() : 0)
                .withErrorOriginService(icmsUnifiedError.originService().toString())
                .withErrorReportingService(icmsUnifiedError.reportingService().toString())
                .withErrorSeverity(icmsUnifiedError.severity().toString())
                .withErrorServiceInternalCode(
                        String.format("%04d", icmsUnifiedError.errorCode().code()))
                .withErrorDescription(icmsUnifiedError.errorDescription());

        unifiedErrorMetric = applyUninifiedErrorData(unifiedErrorMetric, unifiedErrorData);

        telemetryEventClient.triggerErrorEvent(List.of(unifiedErrorMetric));
    }

    private UnifiedErrorMetric applyUninifiedErrorData(
            @NotNull UnifiedErrorMetric unifiedErrorMetric,
            @Nullable UnifiedErrorData unifiedErrorData) {
        if (unifiedErrorData != null) {
            if (StringUtils.isNotBlank(unifiedErrorData.getRequestId())) {
                unifiedErrorMetric.withRequestId(unifiedErrorData.getRequestId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getInstanceId())) {
                unifiedErrorMetric.withInstanceId(unifiedErrorData.getInstanceId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getFunctionId())) {
                unifiedErrorMetric.withFunctionId(unifiedErrorData.getFunctionId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getFunctionVersionId())) {
                unifiedErrorMetric.withFunctionVersionId(unifiedErrorData.getFunctionVersionId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getTaskId())) {
                unifiedErrorMetric.withTaskId(unifiedErrorData.getTaskId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getDeploymentId())) {
                unifiedErrorMetric.withDeploymentId(unifiedErrorData.getDeploymentId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getGpuSpecificationId())) {
                unifiedErrorMetric.withGpuSpecificationId(unifiedErrorData.getGpuSpecificationId());
            }
            if (StringUtils.isNotBlank(unifiedErrorData.getNcaId())) {
                unifiedErrorMetric.withNcaId(unifiedErrorData.getNcaId());
            }
        }
        return unifiedErrorMetric;
    }

    private static String getMessageForUnifiedError(@NotNull DescriptiveUnifiedError error) {
        return String.format(
                "OriginService: %s | Severity: %s | ReportingService: %s | ErrorCode: %04d | ErrorDescription: %s",
                error.originService(),
                error.severity(),
                error.reportingService(),
                error.errorCode().code(),
                error.errorDescription());
    }
}
