# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

@0xcccc1d2d6dc1a2ac;

struct Message {
    union {
        headers  @0 :Map(Text, Text);
        # Request MUST start by sending headers.
        # Request headers MUST contain "path" key, to indicate the RPC path.
        # Request headers MUST contain "content-type" key, to indicate the content type of the body.
        # These two headers ensure the server knows how to handle the request.
        # Response MUST start by sending headers.
        # Response headers MUST contain "status_code" key to indicate initial status code.
        # Response headers status_code = 200 indicates stream is open and more data will follow.

        body @1 :Body;
        # Request and Response can send multiple body chunks

        trailers @2 :Map(Text, Text);
        # Request MAY send trailers as last message.
        # Request trailers MAY be empty depending on application needs.
        # Response MUST send trailers as last message.
        # Response trailers MUST contain "status_code" key to indicate final status code.
        # Response trailers status_code = 200 indicates success.
    }
}

struct Body {
    content @0 :Data;
    # Primary data blob that will be MUST be encoded based on "content-type"
}

struct Map(Key, Value) {
  entries @0 :List(Entry);
  struct Entry {
    key @0 :Key;
    value @1 :Value;
  }
}

struct Handshake {
    inferenceServerId @0 :Text;
    authToken         @1 :Text;
    # Null (unset) when the client has no auth token configured.
    # Checked via has_auth_token() on the reader side.
}

struct HandshakeAck {
    accepted @0 :Bool;
    reason   @1 :Text;
    # Populated when accepted=false to describe why the handshake was rejected.
}
