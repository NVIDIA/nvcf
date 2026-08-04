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
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-test/deep"
	"github.com/hashicorp/vault/sdk/logical"
	"gopkg.in/square/go-jose.v2"
)

func FetchJWKS(b *backend, storage *logical.Storage) (*jose.JSONWebKeySet, error) {

	req := &logical.Request{
		Operation:  logical.ReadOperation,
		Path:       "jwks",
		Storage:    *storage,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		return nil, err
	}
	if resp.IsError() {
		return nil, resp.Error()
	}

	rawBody, ok := resp.Data[logical.HTTPRawBody].([]byte)
	if !ok {
		return nil, errors.New("no raw body returned")
	}

	jwkSet := jose.JSONWebKeySet{}
	err = json.Unmarshal(rawBody, &jwkSet)
	if err != nil {
		return nil, errors.New("cannot unmarshal body to JSONWebKeySet")
	}

	return &jwkSet, nil
}

func TestJwks(t *testing.T) {
	b, storage := getTestBackend(t)

	err := writeRole(b, storage, "tester", "tester.example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	jwkSet, err := FetchJWKS(b, storage)
	if err != nil {
		t.Fatalf("err:%s\n", err)
	}

	expectedKeySet, err := b.getPublicKeys(context.Background(), *storage, "test")
	if err != nil {
		t.Fatalf("err: %#v", err)
	}

	for i, ek := range expectedKeySet.Keys {
		data, _ := json.Marshal(ek)
		var nek jose.JSONWebKey
		if json.Unmarshal(data, &nek) != nil {
			t.Fatalf("Unable to transcode key")
		}
		expectedKeySet.Keys[i] = nek
	}

	if len(expectedKeySet.Keys) == 0 {
		t.Fatal("Expected at least one key to be present.")
	}

	if diff := deep.Equal(expectedKeySet, jwkSet); diff != nil {
		t.Error(diff)
	}
}
