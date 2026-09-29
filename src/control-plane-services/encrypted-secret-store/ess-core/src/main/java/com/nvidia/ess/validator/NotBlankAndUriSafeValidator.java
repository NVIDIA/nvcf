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
package com.nvidia.ess.validator;

import jakarta.validation.ConstraintValidator;
import jakarta.validation.ConstraintValidatorContext;
import java.net.URLEncoder;
import java.nio.charset.Charset;
import java.util.Objects;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;

@Slf4j
@Component
public class NotBlankAndUriSafeValidator implements ConstraintValidator<NotBlankAndUriSafe, String>  {

    @Setter(onMethod_ = @Autowired)
    private NotBlankAndUriSafeValidationHelper helper;

    @Override
    public boolean isValid(String field, ConstraintValidatorContext context) {
        return helper.notBlankAndUriSafe(field,
                Objects.isNull(field) ? null : URLEncoder.encode(field, Charset.defaultCharset()));
    }
}
