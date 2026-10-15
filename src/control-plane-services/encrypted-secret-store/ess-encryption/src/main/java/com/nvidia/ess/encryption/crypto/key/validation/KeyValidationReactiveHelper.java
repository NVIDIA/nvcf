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
package com.nvidia.ess.encryption.crypto.key.validation;

import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_ENCRYPTED_AT_KEY;
import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_KID_KEY;
import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_NAMESPACE_KEY;
import static com.nvidia.ess.encryption.constants.EncryptionOpenTelemetryAttributes.EK_VALIDATION_ERROR_KEY_KEY;
import static com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry.maskKid;

import com.nvidia.ess.encryption.constants.KeyFetchErrorCode;
import com.nvidia.ess.encryption.metrics.EncryptionMetricsRegistry;
import com.nvidia.ess.encryption.persistence.models.EncryptionKeyModel;
import com.nvidia.ess.encryption.util.TracingUtils;
import io.opentelemetry.api.trace.Span;
import java.time.Instant;
import lombok.Setter;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Component;
import reactor.util.context.ContextView;

@Component
public class KeyValidationReactiveHelper {
    @Setter(onMethod_ = {@Autowired})
    private EncryptionMetricsRegistry encryptionMetricsRegistry;

    public void recordValidationErrorTelemetry(ContextView contextView, EncryptionKeyModel model, KeyFetchErrorCode errorCode) {
        Instant encryptedAt = model.getEncryptedAt();

        encryptionMetricsRegistry.recordNekValidationError(model.getNamespace(), model.getKid(),
            errorCode);
        TracingUtils.setSpanAttribute(contextView, EK_VALIDATION_ERROR_KEY_KEY, errorCode.name());
        TracingUtils.setSpanAttribute(contextView, EK_NAMESPACE_KEY, model.getNamespace());
        TracingUtils.setSpanAttribute(contextView, EK_KID_KEY, maskKid(model.getKid()));

        // TODO keeping backwards compatible for now. If there is no OTEL javaagent, it is noop
        //  when removing, remove all WithSpan annotated methods
        Span.current()
                .setAttribute(EK_VALIDATION_ERROR_KEY_KEY, errorCode.name())
                .setAttribute(EK_NAMESPACE_KEY, model.getNamespace())
                .setAttribute(EK_KID_KEY, maskKid(model.getKid()));

        if (encryptedAt != null) {
            TracingUtils.setSpanAttribute(contextView, EK_ENCRYPTED_AT_KEY, encryptedAt.toString());
            Span.current()
                .setAttribute(EK_ENCRYPTED_AT_KEY, encryptedAt.toString());
        }
    }
}
