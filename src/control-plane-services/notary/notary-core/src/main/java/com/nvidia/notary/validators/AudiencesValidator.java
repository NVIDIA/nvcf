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
package com.nvidia.notary.validators;

import com.nvidia.boot.exceptions.BadRequestException;
import java.util.List;
import lombok.RequiredArgsConstructor;
import org.apache.commons.collections4.CollectionUtils;
import org.springframework.stereotype.Service;

@Service
@RequiredArgsConstructor
public class AudiencesValidator {

    /**
     * Validates a list of target-service identifiers used for audience binding. The list must be
     * non-empty.
     *
     * @param audienceServiceIds list of ids to validate.
     *
     * @return validated list.
     */
    public List<String> getValidatedAudiences(List<String> audienceServiceIds) {
        if (CollectionUtils.isEmpty(audienceServiceIds)) {
            throw new BadRequestException("Audience services must have at least one audience");
        }
        return audienceServiceIds;
    }


}
