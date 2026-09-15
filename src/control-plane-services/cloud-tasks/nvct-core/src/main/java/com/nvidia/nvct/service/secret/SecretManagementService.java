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
package com.nvidia.nvct.service.secret;

import static com.nvidia.nvct.util.NvctConstants.NGC_API_KEY;

import tools.jackson.databind.JsonNode;
import com.nvidia.nvct.rest.task.dto.SecretDto;
import java.util.Optional;
import java.util.Set;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class SecretManagementService {

    public static Optional<JsonNode> getNgcApiKey(Set<SecretDto> secrets) {
        return secrets.stream()
                .filter(secret -> secret.name().equalsIgnoreCase(NGC_API_KEY))
                .map(SecretDto::value)
                .findFirst();
    }

    public static boolean hasDupeSecrets(Set<SecretDto> secrets) {
        var dedupedCount = (Long) secrets.stream().map(SecretDto::name).distinct().count();
        return dedupedCount != secrets.size();
    }

}
