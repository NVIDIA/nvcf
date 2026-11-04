// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

use std::path::PathBuf;

use anyhow::{Context, Result};
use clap::Parser;

#[derive(Debug, Parser)]
#[command(name = "stargate-generate-tls-cert-probe")]
struct Args {
    #[arg(long = "dns-name", value_name = "NAME", required = true)]
    dns_names: Vec<String>,

    #[arg(long, value_name = "PATH")]
    cert_path: PathBuf,

    #[arg(long, value_name = "PATH")]
    key_path: PathBuf,
}

fn generate_certificate(args: &Args) -> Result<()> {
    let (cert_pem, key_pem) =
        stargate_tls::generate_self_signed_cert_for_names(args.dns_names.clone())?;
    std::fs::write(&args.cert_path, cert_pem)
        .with_context(|| format!("write certificate {}", args.cert_path.display()))?;
    std::fs::write(&args.key_path, key_pem)
        .with_context(|| format!("write private key {}", args.key_path.display()))?;
    Ok(())
}

fn main() -> Result<()> {
    generate_certificate(&Args::parse())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cli_requires_at_least_one_dns_name() {
        let error = Args::try_parse_from([
            "stargate-generate-tls-cert-probe",
            "--cert-path=/tmp/cert.pem",
            "--key-path=/tmp/key.pem",
        ])
        .expect_err("certificate generation without a DNS name must fail");

        assert_eq!(
            error.kind(),
            clap::error::ErrorKind::MissingRequiredArgument
        );
    }

    #[test]
    fn repeated_dns_names_generate_parseable_pem_files() {
        let temp_dir = tempfile::tempdir().unwrap();
        let cert_path = temp_dir.path().join("tls.crt");
        let key_path = temp_dir.path().join("tls.key");
        let args = Args::try_parse_from([
            "stargate-generate-tls-cert-probe",
            "--dns-name=stargate-0.example",
            "--dns-name=stargate-1.example",
            &format!("--cert-path={}", cert_path.display()),
            &format!("--key-path={}", key_path.display()),
        ])
        .unwrap();

        generate_certificate(&args).unwrap();

        let identity = stargate_tls::ServerTlsIdentity::from_optional_pem(
            Some(std::fs::read(cert_path).unwrap()),
            Some(std::fs::read(key_path).unwrap()),
        )
        .unwrap();
        let (cert_pem, key_pem) = identity.pem_pair().unwrap();
        assert!(cert_pem.starts_with(b"-----BEGIN CERTIFICATE-----"));
        assert!(key_pem.starts_with(b"-----BEGIN PRIVATE KEY-----"));
    }
}
