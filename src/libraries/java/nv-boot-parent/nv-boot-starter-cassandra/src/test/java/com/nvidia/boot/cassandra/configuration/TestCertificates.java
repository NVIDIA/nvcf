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

package com.nvidia.boot.cassandra.configuration;

import java.math.BigInteger;
import java.security.GeneralSecurityException;
import java.security.KeyPairGenerator;
import java.security.SecureRandom;
import java.time.Instant;
import java.util.Base64;
import java.util.Date;
import org.bouncycastle.asn1.x500.X500Name;
import org.bouncycastle.cert.jcajce.JcaX509CertificateConverter;
import org.bouncycastle.cert.jcajce.JcaX509v3CertificateBuilder;
import org.bouncycastle.operator.OperatorCreationException;
import org.bouncycastle.operator.jcajce.JcaContentSignerBuilder;

/**
 * Generates self-signed certificates in memory so tests can control the validity window and do not
 * need committed key material.
 */
final class TestCertificates {

    private static final Base64.Encoder PEM_ENCODER =
            Base64.getMimeEncoder(64, "\n".getBytes(java.nio.charset.StandardCharsets.US_ASCII));

    /** PEM-encoded certificate and PKCS#8 private key; both can be used inline as PEM content. */
    record PemMaterial(String certificate, String privateKey) {
    }

    private TestCertificates() {
    }

    static PemMaterial selfSigned(String commonName, Instant notBefore, Instant notAfter) {
        try {
            var keyPairGenerator = KeyPairGenerator.getInstance("RSA");
            keyPairGenerator.initialize(2048);
            var keyPair = keyPairGenerator.generateKeyPair();

            var subject = new X500Name("CN=" + commonName);
            var serial = new BigInteger(64, new SecureRandom());
            var certificateBuilder = new JcaX509v3CertificateBuilder(
                    subject, serial, Date.from(notBefore), Date.from(notAfter), subject,
                    keyPair.getPublic());
            var signer = new JcaContentSignerBuilder("SHA256withRSA").build(keyPair.getPrivate());
            var certificate = new JcaX509CertificateConverter()
                    .getCertificate(certificateBuilder.build(signer));

            return new PemMaterial(
                    toPem("CERTIFICATE", certificate.getEncoded()),
                    toPem("PRIVATE KEY", keyPair.getPrivate().getEncoded()));
        } catch (GeneralSecurityException | OperatorCreationException e) {
            throw new IllegalStateException("Failed to generate test certificate", e);
        }
    }

    private static String toPem(String type, byte[] der) {
        return "-----BEGIN " + type + "-----\n"
                + PEM_ENCODER.encodeToString(der) + "\n"
                + "-----END " + type + "-----\n";
    }
}
