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

//! nvstor KMS — key management service.
//!
//! Provides:
//! - Three-layer key hierarchy: Master Key (KEK) → Data Encryption Key (DEK) → data
//! - Envelope encryption: DEK wrapped by KEK, stored alongside encrypted data
//! - Key rotation without re-encryption (old KEK versions kept for unwrapping)
//! - Pluggable backends (local RocksDB, future: Vault, AWS KMS)
//! - AES-256-GCM authenticated encryption
//! - Zeroize for sensitive key material

pub mod backend;
pub mod envelope;
pub mod manager;
pub mod types;
