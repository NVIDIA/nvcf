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

const ENDPOINT = '/v1/chat/completions'

export const options = {
  insecureSkipTLSVerify: skipTlsVerify(),
  thresholds: thresholds,
}

// k6 buffers the whole server-sent-event stream before returning, so
// http_req_duration here is time to last token, not time to first token.
// Measuring time to first token needs a k6 binary built with xk6-sse.
export default function () {
  const payload = JSON.stringify({
    model: modelId(),
    messages: [{ role: 'user', content: 'What should I see in Paris?' }],
    temperature: 0.2,
    top_p: 0.7,
    max_tokens: 256,
    stream: true,
  })

  const response = http.post(
    baseUrl() + ENDPOINT,
    payload,
    params(ENDPOINT, { 'Accept': 'text/event-stream' })
  )
  classify(response, ENDPOINT)

  check(response, {
    'status is 200': (r) => r.status === 200,
    'body is an event stream': (r) => r.status === 200 && String(r.body).includes('data:'),
    'stream terminated': (r) => r.status === 200 && String(r.body).includes('[DONE]'),
  })
}
