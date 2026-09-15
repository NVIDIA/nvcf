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
package com.nvidia.ess.encryption.constants;

import lombok.experimental.UtilityClass;

@UtilityClass
public final class Constants {

    public static final String MSG_NAMESPACE_MISMATCHED = "integrity-checks: namespace mismatched found %s expected %s";
    public static final String MSG_KID_MISMATCHED= "integrity-checks: kid mismatched found %s expected %s";
    public static final String MSG_CREATED_AT_MISMATCHED= "integrity-checks: createdAt mismatched found %s expected %s";
    public static final String MSG_ENCRYPTED_AT_MISMATCHED= "integrity-checks: encryptedAt mismatched found %d expected %d";
    public static final String MSG_ENCRYPTED_BY_KID_MISMATCHED= "integrity-checks: encryptedByKid mismatched found %s expected %s";
    public static final String MSG_TYPE_MISMATCH = "integrity-checks: type mismatch for field=%s, found %s expected %s";
    public static final String MSG_OBJ_NULL = "integrity-checks: object %s is null";

    public static final String TRACE_ONLY_NAME = "method.trace.only";
}
