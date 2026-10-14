/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package dsl

import "regexp"

const redactedSecret = "[redacted]"

// secretPattern matches API key values (nvapi-...) and JWT-shaped tokens, the
// credentials a response or an expected value can carry into a failure message.
var secretPattern = regexp.MustCompile(`nvapi-[A-Za-z0-9._-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// RedactSecrets masks API keys and JWTs in text that is about to be shown in a
// failure message.
func RedactSecrets(text string) string {
	return secretPattern.ReplaceAllString(text, redactedSecret)
}
