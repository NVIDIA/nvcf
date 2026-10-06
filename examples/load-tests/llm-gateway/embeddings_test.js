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

import { check } from 'k6'
import http from 'k6/http'

import {
  baseUrl,
  classify,
  modelId,
  params,
  skipTlsVerify,
  thresholds,
} from './lib/common.js'

const ENDPOINT = '/v1/embeddings'

export const options = {
  insecureSkipTLSVerify: skipTlsVerify(),
  thresholds: thresholds,
}

export default function () {
  const payload = JSON.stringify({
    model: modelId(),
    input: 'What should I see in Paris?',
    encoding_format: 'float',
  })

  const response = http.post(baseUrl() + ENDPOINT, payload, params(ENDPOINT))
  classify(response, ENDPOINT)

  check(response, {
    'status is 200': (r) => r.status === 200,
    'has embedding data': (r) => {
      if (r.status !== 200) {
        return false
      }
      try {
        const data = r.json('data')
        return Array.isArray(data) && data.length > 0
      } catch (err) {
        return false
      }
    },
  })
}
