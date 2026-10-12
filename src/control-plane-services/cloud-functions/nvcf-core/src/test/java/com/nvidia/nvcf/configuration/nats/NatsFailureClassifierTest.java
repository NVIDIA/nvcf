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
package com.nvidia.nvcf.configuration.nats;

import static org.assertj.core.api.Assertions.assertThat;

import io.nats.client.AuthenticationException;
import java.io.IOException;
import java.net.ConnectException;
import java.net.SocketTimeoutException;
import java.security.cert.CertPathValidatorException;
import java.security.cert.CertificateException;
import java.security.cert.CertificateExpiredException;
import javax.net.ssl.SSLException;
import javax.net.ssl.SSLHandshakeException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;

class NatsFailureClassifierTest {

    @Test
    void expiredCertificateWrappedInHandshakeFailureIsClassifiedAsExpired() {
        var error = new IOException("connect failed",
                                    new SSLHandshakeException("handshake failed") {
                                        {
                                            initCause(new CertificateExpiredException());
                                        }
                                    });

        assertThat(NatsFailureClassifier.classify(error))
                .isEqualTo(NatsFailureReason.TLS_CERT_EXPIRED);
    }

    @Test
    void expiredCertPathValidationIsClassifiedAsExpired() {
        var reason = new CertPathValidatorException("expired", null, null, -1,
                                                    CertPathValidatorException.BasicReason.EXPIRED);

        assertThat(NatsFailureClassifier.classify(new SSLException(reason)))
                .isEqualTo(NatsFailureReason.TLS_CERT_EXPIRED);
    }

    @Test
    void untrustedCertificateIsClassifiedAsVerificationFailure() {
        var chain = new CertPathValidatorException("unable to find valid certification path");

        assertThat(NatsFailureClassifier.classify(new SSLHandshakeException("x") {
            {
                initCause(chain);
            }
        })).isEqualTo(NatsFailureReason.TLS_VERIFICATION);
        assertThat(NatsFailureClassifier.classify(new SSLException(new CertificateException())))
                .isEqualTo(NatsFailureReason.TLS_VERIFICATION);
    }

    @Test
    void otherSslErrorIsClassifiedAsTls() {
        assertThat(NatsFailureClassifier.classify(new SSLException("protocol error")))
                .isEqualTo(NatsFailureReason.TLS);
    }

    @Test
    void authenticationAndTimeoutAndConnectionErrorsAreDistinguished() {
        assertThat(NatsFailureClassifier.classify(new AuthenticationException("denied")))
                .isEqualTo(NatsFailureReason.AUTHENTICATION);
        assertThat(NatsFailureClassifier.classify(new IOException(new SocketTimeoutException())))
                .isEqualTo(NatsFailureReason.TIMEOUT);
        assertThat(NatsFailureClassifier.classify(new ConnectException("refused")))
                .isEqualTo(NatsFailureReason.CONNECTION);
        assertThat(NatsFailureClassifier.classify(
                new IOException("Unable to connect to NATS servers: nats://x")))
                .isEqualTo(NatsFailureReason.CONNECTION);
    }

    @Test
    void messageOnlyExceptionsFallBackToMessageMatching() {
        assertThat(NatsFailureClassifier.classify(new IOException("TLS handshake failed")))
                .isEqualTo(NatsFailureReason.TLS);
    }

    @Test
    void unknownNonIoErrorIsClassifiedAsOther() {
        assertThat(NatsFailureClassifier.classify(new IllegalStateException("boom")))
                .isEqualTo(NatsFailureReason.OTHER);
        assertThat(NatsFailureClassifier.classify(null)).isEqualTo(NatsFailureReason.OTHER);
    }

    @Test
    void cyclicCauseChainTerminates() {
        var a = new RuntimeException("a");
        var b = new RuntimeException("b", a);
        a.initCause(b);

        assertThat(NatsFailureClassifier.classify(a)).isEqualTo(NatsFailureReason.OTHER);
    }

    @ParameterizedTest
    @CsvSource(delimiter = '|', value = {
            "Authorization Violation|AUTHENTICATION",
            "User Authentication Expired|AUTHENTICATION",
            "authentication token expired|AUTHENTICATION",
            "Permissions Violation for Publish to \"foo.bar\"|AUTHENTICATION",
            "invalid peer certificate: Expired|TLS_CERT_EXPIRED",
            "certificate verify failed|TLS_VERIFICATION",
            "tls: handshake failure|TLS",
            "operation timed out|TIMEOUT",
            "something odd|OTHER"
    })
    void serverErrorMessagesAreClassified(String message, NatsFailureReason expected) {
        assertThat(NatsFailureClassifier.classifyMessage(message)).isEqualTo(expected);
    }

    @Test
    void nullMessageIsOther() {
        assertThat(NatsFailureClassifier.classifyMessage(null)).isEqualTo(NatsFailureReason.OTHER);
    }
}
