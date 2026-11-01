#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -xe

pushd "$(dirname "$0")"
openssl req -new -newkey rsa:4096 -sha256 -x509 -nodes -days 365 -out certs/cert.pem.tmp -keyout certs/key.pem.tmp -config req.csr -extensions 'v3_req'
mv certs/cert.pem.tmp certs/cert.pem
mv certs/key.pem.tmp certs/key.pem
popd
