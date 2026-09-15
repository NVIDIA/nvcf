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
public class EncryptionOpenTelemetryAttributes {
    // Attribute keys.
    public static final String EK_NAMESPACE_KEY = "ek.namespace";
    public static final String EK_KID_KEY = "ek.kid";
    public static final String EK_ENCRYPTED_AT_KEY = "ek.encrypted_at";
    public static final String EK_VALIDATION_ERROR_KEY_KEY = "ek.validation.error_key";
}
