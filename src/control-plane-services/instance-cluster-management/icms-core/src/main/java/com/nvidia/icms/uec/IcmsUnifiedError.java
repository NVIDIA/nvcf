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


import com.nvidia.icms.uec.internal.v2.DescriptiveUnifiedError;
import com.nvidia.icms.uec.internal.v2.UnifiedError;
import lombok.RequiredArgsConstructor;
import lombok.experimental.Delegate;
import org.apache.commons.lang3.StringUtils;

@RequiredArgsConstructor
public enum IcmsUnifiedError implements DescriptiveUnifiedError {
    // ICMS General Errors
    ICMS_UNDEFINED(
            UnifiedError.from(0x8BE20000),
            "This is a default ICMS UEC for unknown/unmapped errors",
            "ICMS undefined error"
    ),

    // external controllers (NVCF, NGC, etc)

    // NVCF/NGC/Other clients errors
    NVCF_INCORRECT_PARAMETER(
            UnifiedError.from(0xEBE30001),
           "Incorrect parameter is provided to ICMS by NVCF or other clients",
            "Incorrect parameter from NVCF"
    ),

    NVCF_CUSTOMER_NO_ACCESS_TO_CLUSTERS(
            UnifiedError.from(0xEBE30002),
            "Provided customer does not have access to any clusters",
            "Customer does not have access to any cluster groups"

    ),

    NVCF_CUSTOMER_NO_ACCESS_TO_GPU(
            UnifiedError.from(0xEBE30003),
            "Customer does not have access to requested GPU",
            "Customer does not have access to GPU %s"
    ),

    NVCF_CUSTOMER_NO_ACCESS_TO_INSTANCE_TYPE(
            UnifiedError.from(0xEBE30004),
            "Customer does not have access to requested instance type",
            "Customer does not have access to instance type %s for gpu %s"
    ),

    NVCF_CUSTOMER_NO_ACCESS_TO_READY_CLUSTER(
            UnifiedError.from(0xEBE30005),
            "Customer does not have access to any healthy and ready cluster",
            "Customer does not have access to any ready cluster for GPU: %s and instance type: %s"
    ),


    ;

    @Delegate
    private final UnifiedError unifiedError;
    private final String description;
    private final String defaultMessageFormat;

    @Override
    public String errorName() {

        return name();
    }

    @Override
    public String errorDescription() {

        return description;
    }

    public String defaultMessageFormat() {
        return StringUtils.isBlank(defaultMessageFormat) ? "" : defaultMessageFormat;
    }
}
