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

import java.util.UUID;
import jakarta.annotation.Nullable;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;
import org.apache.commons.lang3.StringUtils;

@Data
@Builder
@AllArgsConstructor
@NoArgsConstructor
public class UnifiedErrorData {
    private String requestId;
    private String instanceId;

    private String functionId;
    private String functionVersionId;
    private String taskId;

    private String deploymentId;
    private String gpuSpecificationId;
    private String ncaId;

    @Override
    public String toString() {
        String result = "";
        if (StringUtils.isNotBlank(getRequestId())) {
            result += "RequestId: " + getRequestId() + " | ";
        }
        if (StringUtils.isNotBlank(getInstanceId())) {
            result += "InstanceId: " + getInstanceId() + " | ";
        }
        if (StringUtils.isNotBlank(getFunctionId())) {
            result += "FunctionId: " + getFunctionId() + " | ";
        }

        if (StringUtils.isNotBlank(getFunctionVersionId())) {
            result += "FunctionVersionId: " + getFunctionVersionId() + " | ";
        }

        if (StringUtils.isNotBlank(getTaskId())) {
            result += "TaskId: " + getTaskId() + " | ";
        }

        if (StringUtils.isNotBlank(getDeploymentId())) {
            result += "DeploymentId: " + getDeploymentId() + " | ";
        }

        if (StringUtils.isNotBlank(getGpuSpecificationId())) {
            result += "GpuSpecificationId: " + getGpuSpecificationId() + " | ";
        }

        if (StringUtils.isNotBlank(getNcaId())) {
            result += "NcaId: " + getNcaId() + " | ";
        }

        if (StringUtils.isNotBlank(result)) {
            result = result.substring(0, result.length() - 3);
        }
        return result;
    }

    public static String stringFromUuid(@Nullable UUID uuid) {
        if (uuid == null) {
            return null;
        }
        return uuid.toString();
    }

}
