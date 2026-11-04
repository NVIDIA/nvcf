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
package com.nvidia.icms.uec.internal.v2;

import com.nvidia.icms.uec.internal.util.BitMaskUtil;
import com.nvidia.icms.uec.internal.util.NumericalRepresentationUtils;
import com.nvidia.icms.uec.internal.v2.errorcode.ErrorCode;
import com.nvidia.icms.uec.internal.v2.originservice.OriginService;
import com.nvidia.icms.uec.internal.v2.reportingservice.ReportingService;
import com.nvidia.icms.uec.internal.v2.severity.Severity;
import lombok.NonNull;

/**
 * <a href="https://confluence.nvidia.com/display/GNCE/New+Schema+Format">UEC definition</a>.
 * <p>
 * Numeric representation is 32 bits as follows:
 * * 4 bits for {@link Severity} (28-31).
 * * 4 bits for {@link ReportingService} (24-27).
 * * 8 bits for {@link OriginService} (16-23).
 * * 16 bits for {@link ErrorCode} (0-15).
 */
public interface UnifiedError extends NumericalRepresentation {

    int SEVERITY_OFFSET = 28;
    int REPORTING_SERVICE_OFFSET = 24;
    int ORIGIN_SERVICE_OFFSET = 16;
    int ERROR_CODE_OFFSET = 0;

    static UnifiedError from(@NonNull Severity severity,
                             @NonNull ReportingService reportingService,
                             @NonNull OriginService originService,
                             @NonNull ErrorCode errorCode) {
        return new UnifiedErrorValue(
                severity,
                reportingService,
                originService,
                errorCode
        );
    }

    /**
     * Parses given hexCode and returns respective {@link UnifiedError} instance.
     * <br>
     * This method works with both {@link #hexCode()} and {@link #hexCodeShort()}
     * formats.
     * <br>
     * This method is case-insensitive.
     */
    static UnifiedError from(String hexCode) {
        var code = NumericalRepresentationUtils.fromHexCode(hexCode);

        return UnifiedError.from(code);
    }

    /**
     * Parses a 32-bit integer into an instance of {@link UnifiedError}.
     */
    static UnifiedError from(int code) {
        int severity = BitMaskUtil.extractBitRange(code, Severity.BITS, SEVERITY_OFFSET);
        int reportingService = BitMaskUtil.extractBitRange(code, ReportingService.BITS, REPORTING_SERVICE_OFFSET);
        int originService = BitMaskUtil.extractBitRange(code, OriginService.BITS, ORIGIN_SERVICE_OFFSET);
        int errorCode = BitMaskUtil.extractBitRange(code, ErrorCode.BITS, ERROR_CODE_OFFSET);

        return from(
                Severity.from(severity),
                ReportingService.from(reportingService),
                OriginService.from(originService),
                ErrorCode.from(errorCode)
        );
    }

    /**
     * Transforms an instance of {@link UnifiedError} into an 32-bit integer.
     */
    @Override
    default int code() {
        return severity().code() << SEVERITY_OFFSET |
                reportingService().code() << REPORTING_SERVICE_OFFSET |
                originService().code() << ORIGIN_SERVICE_OFFSET |
                errorCode().code() << ERROR_CODE_OFFSET;
    }

    Severity severity();

    ReportingService reportingService();

    OriginService originService();

    ErrorCode errorCode();
}
