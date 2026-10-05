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
package com.nvidia.icms.errors;

import com.nvidia.icms.service.cpu.CpuWorkloadsStatus;

/**
 * A CPU shape was registered or requested while CPU-only workloads are not ready. Mapped to HTTP
 * 409 like other conflicts. The message starts with {@value #ERROR_CODE} so clients can detect it.
 */
public class CpuWorkloadsNotReadyException extends IcmsConflictException {

    public static final String ERROR_CODE = "CPU_WORKLOADS_NOT_READY";

    public CpuWorkloadsNotReadyException(CpuWorkloadsStatus status) {
        super(String.format(
                "%s: CPU workloads are %s on this ICMS (reason: %s). CPU shapes cannot be "
                        + "registered or requested until CPU workloads are ready.",
                ERROR_CODE, status.state().name().toLowerCase(), status.reason()));
    }
}
