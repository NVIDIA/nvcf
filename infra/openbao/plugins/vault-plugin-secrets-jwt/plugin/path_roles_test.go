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
	"fmt"
	"testing"

	"github.com/go-test/deep"

	"github.com/hashicorp/vault/sdk/logical"
)

func writeRole(b *backend, storage *logical.Storage, name string, issuer string, claims map[string]interface{}, headers map[string]interface{}) error {
	data := map[string]interface{}{
		"issuer":  issuer,
		"claims":  claims,
		"headers": headers,
	}

	req := &logical.Request{
		Operation:  logical.CreateOperation,
		Path:       "roles/" + name,
		Storage:    *storage,
		Data:       data,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil || (resp != nil && resp.IsError()) {
		return fmt.Errorf("err:%s resp:%#v", err, resp)
	}

	return nil
}

func readRole(b *backend, storage *logical.Storage, name string) (*logical.Response, error) {

	req := &logical.Request{
		Operation:  logical.ReadOperation,
		Path:       "roles/" + name,
		Storage:    *storage,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil || (resp != nil && resp.IsError()) {
		return nil, fmt.Errorf("err:%s resp:%#v", err, resp)
	}

	return resp, nil
}

func TestCreate(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	resp, err := readRole(b, storage, role)
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	subject := resp.Data[keyIssuer].(string)
	if diff := deep.Equal(role+".example.com", subject); diff != nil {
		t.Error("failed to update subject:", diff)
	}

}

func TestCreateRestrictedAudience(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	resp, err := writeConfig(b, storage, map[string]interface{}{
		keyAudiencePattern: "[a-z]+\\.[a-z]+\\.[a-z]+",
	})
	if err != nil {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	err = writeRole(b, storage, role, role+".example.com", map[string]interface{}{"aud": "invalid audience"}, map[string]interface{}{})
	if err == nil {
		t.Fatalf("create role with non-matching audience pattern succeeded")
	}

	err = writeRole(b, storage, role, role+".example.com", map[string]interface{}{"aud": "audience.example.com"}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%s\n", err)
	}

	resp, err = readRole(b, storage, role)
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	claims, ok := resp.Data[keyClaims].(map[string]interface{})
	if !ok {
		t.Error("failed to read response claims")
	}

	audience, ok := claims["aud"]
	if !ok {
		t.Error("no audience claim found")
	}
	if diff := deep.Equal("audience.example.com", audience); diff != nil {
		t.Error("failed to update audience:", diff)
	}

}

func TestCreateDisallowedOtherClaim(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	// added for nv
	err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{"sub": "allowed"}, map[string]interface{}{})

	// added for nv
	// sub claim is allowed in role's claims field
	// if err == nil {
	// t.Fatalf("Create role should have failed")
	// }

	err = writeRole(b, storage, role, role+".example.com", map[string]interface{}{"foo": "bar"}, map[string]interface{}{})
	if err == nil {
		t.Fatalf("Create role should have failed")
	}

	resp, err := writeConfig(b, storage, map[string]interface{}{"allowed_claims": []string{"foo"}})
	if err != nil {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	err = writeRole(b, storage, role, role+".example.com", map[string]interface{}{"foo": "bar"}, map[string]interface{}{})
	if err != nil {
		t.Errorf("%s\n", err)
	}

}

func TestCreateDisallowedOtherHeader(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{"tid": "not allowed"})
	if err == nil {
		t.Fatalf("Create role should have failed")
	}

	resp, err := writeConfig(b, storage, map[string]interface{}{"allowed_headers": []string{"tid"}})
	if err != nil {
		t.Fatalf("err:%s resp:%#v\n", err, resp)
	}

	err = writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{"tid": "12345"})
	if err != nil {
		t.Errorf("%s\n", err)
	}

}

func TestCreateAudienceAsArray(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	claims := map[string]interface{}{
		"aud": []interface{}{"foo", "bar"},
	}

	if err := writeRole(b, storage, role, role+".example.com", claims, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	resp, err := readRole(b, storage, role)
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	claims, ok := resp.Data[keyClaims].(map[string]interface{})
	if !ok {
		t.Error("failed to read response claims")
	}

	audience, ok := claims["aud"]
	if !ok {
		t.Error("no audience claim found")
	}
	if diff := deep.Equal(claims["aud"], audience); diff != nil {
		t.Error("failed to update audience:", diff)
	}
}

func TestList(t *testing.T) {
	b, storage := getTestBackend(t)

	err := writeRole(b, storage, "tester1", "tester.example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	err = writeRole(b, storage, "tester2", "tester.example.com", map[string]interface{}{}, map[string]interface{}{})
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	req := &logical.Request{
		Operation:  logical.ListOperation,
		Path:       "roles",
		Storage:    *storage,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("%v\n", err)
	}

	keys := resp.Data["keys"].([]string)
	if keys == nil {
		t.Fatalf("Missing keys in list response")
	}

	if diff := deep.Equal(keys, []string{"tester1", "tester2"}); diff != nil {
		t.Error("failed to list roles:", diff)
	}
}

func TestDelete(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	req := &logical.Request{
		Operation:  logical.DeleteOperation,
		Path:       "roles/" + role,
		Storage:    *storage,
		MountPoint: "test",
	}

	if _, err := b.HandleRequest(context.Background(), req); err != nil {
		t.Fatalf("%v\n", err)
	}

	if resp, err := readRole(b, storage, role); err != nil || resp != nil {
		t.Errorf("Should have received empty response but got response: %#v", resp)
	}
}
