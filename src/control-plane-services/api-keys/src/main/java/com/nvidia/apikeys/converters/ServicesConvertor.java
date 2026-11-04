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

package com.nvidia.apikeys.converters;

import com.nvidia.apikeys.dto.services.ServiceDto;
import com.nvidia.apikeys.vo.ServiceVo;
import org.springframework.beans.BeanUtils;
import org.springframework.stereotype.Service;

@Service
public class ServicesConvertor {

    public ServiceDto toDto(ServiceVo vo) {
        ServiceDto dto = new ServiceDto();
        BeanUtils.copyProperties(vo, dto);
        return dto;
    }

}
