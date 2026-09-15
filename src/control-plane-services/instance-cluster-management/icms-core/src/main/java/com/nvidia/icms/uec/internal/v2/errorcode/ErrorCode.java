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
package com.nvidia.icms.uec.internal.v2.errorcode;


import com.nvidia.icms.uec.internal.v2.NumericalRepresentation;
import com.nvidia.icms.uec.internal.v2.UnifiedError;

/**
 * UEC Error Code.
 * <p>
 * No predefined values available. Users must define their own service specific error codes.
 */
@FunctionalInterface
public interface ErrorCode extends NumericalRepresentation {

    int BITS = 16;

    static ErrorCode from(int code) {
        return new ErrorCodeValue(code);
    }

    /**
     * 16 bits that define error code of {@link UnifiedError}.
     */
    @Override
    int code();
}
