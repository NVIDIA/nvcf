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

import io.nats.client.AuthenticationException;
import java.io.IOException;
import java.net.SocketTimeoutException;
import java.security.cert.CertPathValidatorException;
import java.security.cert.CertificateException;
import java.security.cert.CertificateExpiredException;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.concurrent.TimeoutException;
import java.util.regex.Pattern;
import javax.net.ssl.SSLException;

/** Maps NATS client errors onto a small, fixed set of {@link NatsFailureReason}s. */
public final class NatsFailureClassifier {

    private static final int MAX_CAUSE_DEPTH = 10;
    // short terms must stand alone (optionally versioned, e.g. TLSv1.3) so "classloader" does
    // not match "ssl"
    private static final Pattern TLS_TERM =
            Pattern.compile("(?<![a-z])(?:tls|ssl)(?:v?\\d[\\d.]*)?(?![a-z])|(?<![a-z])handshake");

    private NatsFailureClassifier() {
    }

    public static NatsFailureReason classify(Throwable error) {
        List<Throwable> chain = causeChain(error);
        if (chain.stream().anyMatch(NatsFailureClassifier::isCertExpired)) {
            return NatsFailureReason.TLS_CERT_EXPIRED;
        }
        if (chain.stream().anyMatch(CertificateException.class::isInstance)
                || chain.stream().anyMatch(CertPathValidatorException.class::isInstance)) {
            return NatsFailureReason.TLS_VERIFICATION;
        }
        if (chain.stream().anyMatch(SSLException.class::isInstance)) {
            return NatsFailureReason.TLS;
        }
        if (chain.stream().anyMatch(AuthenticationException.class::isInstance)) {
            return NatsFailureReason.AUTHENTICATION;
        }
        if (chain.stream().anyMatch(NatsFailureClassifier::isTimeout)) {
            return NatsFailureReason.TIMEOUT;
        }
        for (Throwable cause : chain) {
            NatsFailureReason fromMessage = matchMessage(cause.getMessage());
            if (fromMessage != null) {
                return fromMessage;
            }
        }
        return chain.stream().anyMatch(IOException.class::isInstance)
                ? NatsFailureReason.CONNECTION
                : NatsFailureReason.OTHER;
    }

    /** Classifies a server error string, which is all the client gives for runtime errors. */
    public static NatsFailureReason classifyMessage(String message) {
        NatsFailureReason reason = matchMessage(message);
        return reason != null ? reason : NatsFailureReason.OTHER;
    }

    private static NatsFailureReason matchMessage(String message) {
        if (message == null) {
            return null;
        }
        String msg = message.toLowerCase(Locale.ROOT);
        if (msg.contains("authorization")
                || msg.contains("authentication")
                || msg.contains("permissions violation")) {
            return NatsFailureReason.AUTHENTICATION;
        }
        if (msg.contains("certificate") && msg.contains("expired")) {
            return NatsFailureReason.TLS_CERT_EXPIRED;
        }
        if (msg.contains("certificate")) {
            return NatsFailureReason.TLS_VERIFICATION;
        }
        if (TLS_TERM.matcher(msg).find()) {
            return NatsFailureReason.TLS;
        }
        if (msg.contains("timed out") || msg.contains("timeout")) {
            return NatsFailureReason.TIMEOUT;
        }
        return null;
    }

    private static boolean isCertExpired(Throwable error) {
        return error instanceof CertificateExpiredException
                || (error instanceof CertPathValidatorException pathError
                        && pathError.getReason() == CertPathValidatorException.BasicReason.EXPIRED);
    }

    private static boolean isTimeout(Throwable error) {
        return error instanceof SocketTimeoutException || error instanceof TimeoutException;
    }

    private static List<Throwable> causeChain(Throwable error) {
        List<Throwable> chain = new ArrayList<>();
        for (Throwable cause = error;
                cause != null && chain.size() < MAX_CAUSE_DEPTH && !chain.contains(cause);
                cause = cause.getCause()) {
            chain.add(cause);
        }
        return chain;
    }
}
