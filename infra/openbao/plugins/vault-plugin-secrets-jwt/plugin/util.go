//
// Copyright 2021 Outfox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package jwtsecrets

import (
	"crypto"
	"encoding/base64"
	"path"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// uniqueIdGenerator is an interface for generating unique ids.
type uniqueIdGenerator interface {
	id() (string, error)
}

// friendlyIdGenerator generates friendly-id formatted UUIDs.
type friendlyIdGenerator struct{}

func (fid friendlyIdGenerator) id() (string, error) {
	// NewRandom (v4), not NewUUID (v1). A v1 UUID encodes the host MAC address
	// and the creation timestamp, and this id is published as the token's jti,
	// so v1 would leak the signer's hardware address and issue time to every
	// holder of a token.
	generatedUUID, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	return encodeBase62UUID(generatedUUID), nil
}

// fakeIDGenerator generates a predictable sequence of numeric ids for testing.
type fakeIDGenerator struct {
	Counter int
}

func (f *fakeIDGenerator) id() (string, error) {
	f.Counter++
	return strconv.Itoa(f.Counter), nil
}

func intMax(x int, y int) int {
	if x > y {
		return x
	}
	return y
}

func durationMin(x time.Duration, y time.Duration) time.Duration {
	if x < y {
		return x
	}
	return y
}

func createKeyId(backendId string, policyName string, version int) string {

	rawId := path.Join(backendId, policyName, strconv.Itoa(version))

	hasher := crypto.SHA1.New()
	hasher.Write([]byte(rawId))

	return base64.RawURLEncoding.EncodeToString(hasher.Sum(nil))
}
