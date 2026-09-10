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

const ENDPOINT = '/v1/responses'

export const options = {
  insecureSkipTLSVerify: skipTlsVerify(),
  thresholds: thresholds,
}

export default function () {
  const payload = JSON.stringify({
    model: modelId(),
    input: 'What should I see in Paris?',
    max_output_tokens: 256,
    temperature: 0.2,
    top_p: 0.7,
    stream: false,
  })

  const response = http.post(baseUrl() + ENDPOINT, payload, params(ENDPOINT))
  classify(response, ENDPOINT)

  check(response, {
    'status is 200': (r) => r.status === 200,
    'has output': (r) => {
      if (r.status !== 200) {
        return false
      }
      try {
        const output = r.json('output')
        const outputText = r.json('output_text')
        return (output !== undefined && output !== null) ||
          (outputText !== undefined && outputText !== null)
      } catch (err) {
        return false
      }
    },
  })
}
