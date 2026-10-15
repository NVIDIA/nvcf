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
	"time"

	"github.com/go-test/deep"
	"github.com/hashicorp/vault/sdk/logical"
	"gopkg.in/square/go-jose.v2/jwt"
)

func getSignedTokenWithClaims(b *backend, storage *logical.Storage, role string, claims map[string]interface{}, headers map[string]interface{}, claimsDest interface{}, headersDest map[string]interface{}) error {
	data := map[string]interface{}{
		"claims":  claims,
		"headers": headers,
	}

	req := &logical.Request{
		Operation:  logical.UpdateOperation,
		Path:       "sign/" + role,
		Storage:    *storage,
		Data:       data,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil || (resp != nil && resp.IsError()) {
		return fmt.Errorf("err:%s resp:%#v", err, resp)
	}

	rawToken, ok := resp.Data["token"]
	if !ok {
		return fmt.Errorf("no returned token")
	}

	strToken, ok := rawToken.(string)
	if !ok {
		return fmt.Errorf("token was %T, not a string", rawToken)
	}

	token, err := jwt.ParseSigned(strToken)
	if err != nil {
		return fmt.Errorf("error parsing jwt: %s", err)
	}

	publicKeys, err := FetchJWKS(b, storage)
	if err != nil {
		return fmt.Errorf("error retrieving public keys: %s", err)
	}

	matchingPublicKeys := publicKeys.Key(token.Headers[0].KeyID)
	if len(matchingPublicKeys) != 1 {
		return fmt.Errorf("error locating unique public keys: %s", err)
	}

	if headersDest != nil {
		for header := range token.Headers[0].ExtraHeaders {
			headersDest[string(header)] = token.Headers[0].ExtraHeaders[header]
		}
	}

	var targetClaims interface{}
	if claimsDest != nil {
		targetClaims = claimsDest
	} else {
		targetClaims = &jwt.Claims{}
	}

	if err = token.Claims(matchingPublicKeys[0], targetClaims); err != nil {
		return fmt.Errorf("error decoding claims: %s", err)
	}

	return nil
}

func getSignedTokenWithoutClaims(b *backend, storage *logical.Storage, role string, claimsDest any, headersDest map[string]any) error {

	req := &logical.Request{
		Operation:  logical.ReadOperation,
		Path:       "sign/" + role,
		Storage:    *storage,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err != nil || (resp != nil && resp.IsError()) {
		return fmt.Errorf("err:%s resp:%#v", err, resp)
	}

	rawToken, ok := resp.Data["token"]
	if !ok {
		return fmt.Errorf("no returned token")
	}

	strToken, ok := rawToken.(string)
	if !ok {
		return fmt.Errorf("token was %T, not a string", rawToken)
	}

	token, err := jwt.ParseSigned(strToken)
	if err != nil {
		return fmt.Errorf("error parsing jwt: %s", err)
	}

	publicKeys, err := FetchJWKS(b, storage)
	if err != nil {
		return fmt.Errorf("error retrieving public keys: %s", err)
	}

	matchingPublicKeys := publicKeys.Key(token.Headers[0].KeyID)
	if len(matchingPublicKeys) != 1 {
		return fmt.Errorf("error locating unique public keys: %s", err)
	}

	if headersDest != nil {
		for header := range token.Headers[0].ExtraHeaders {
			headersDest[string(header)] = token.Headers[0].ExtraHeaders[header]
		}
	}

	var targetClaims interface{}
	if claimsDest != nil {
		targetClaims = claimsDest
	} else {
		targetClaims = &jwt.Claims{}
	}

	if err = token.Claims(matchingPublicKeys[0], targetClaims); err != nil {
		return fmt.Errorf("error decoding claims: %s", err)
	}

	return nil
}

func TestSignWrite(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	claims := map[string]interface{}{
		"sub": "Kif Kroker",
		"aud": "Zapp Brannigan",
	}

	var decoded jwt.Claims
	if err := getSignedTokenWithClaims(b, storage, role, claims, map[string]interface{}{}, &decoded, nil); err != nil {
		t.Fatalf("%v\n", err)
	}

	if decoded.Expiry.Time().After(time.Now().Add((3 * time.Minute) + (1 * time.Second))) {
		t.Errorf("expiry is too far in the future")
	}
	if decoded.Expiry.Time().Before(time.Now().Add((3 * time.Minute) - (1 * time.Second))) {
		t.Errorf("expiry is too far in the past")
	}
	decoded.Expiry = nil

	if decoded.IssuedAt.Time().After(time.Now().Add(1 * time.Second)) {
		t.Errorf("issued at is too far in the future")
	}
	if decoded.IssuedAt.Time().Before(time.Now().Add(-1 * time.Second)) {
		t.Errorf("issued at is too far in the past")
	}
	decoded.IssuedAt = nil

	if decoded.NotBefore.Time().After(time.Now().Add(1 * time.Second)) {
		t.Errorf("not before is too far in the future")
	}
	if decoded.NotBefore.Time().Before(time.Now().Add(-1 * time.Second)) {
		t.Errorf("not before is too far in the past")
	}
	decoded.NotBefore = nil

	expectedClaims := jwt.Claims{
		Subject:  "Kif Kroker",
		Audience: []string{"Zapp Brannigan"},
		ID:       "1",
		Issuer:   role + ".example.com",
	}

	if diff := deep.Equal(expectedClaims, decoded); diff != nil {
		t.Error(diff)
	}
}

func TestSignRead(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	claims := map[string]any{
		"sub": "Kif Kroker",
		"aud": "Zapp Brannigan",
	}

	if err := writeRole(b, storage, role, role+".example.com", claims, map[string]any{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	var decoded jwt.Claims
	if err := getSignedTokenWithoutClaims(b, storage, role, &decoded, nil); err != nil {
		t.Fatalf("%v\n", err)
	}

	if decoded.Expiry.Time().After(time.Now().Add((3 * time.Minute) + (1 * time.Second))) {
		t.Errorf("expiry is too far in the future")
	}
	if decoded.Expiry.Time().Before(time.Now().Add((3 * time.Minute) - (1 * time.Second))) {
		t.Errorf("expiry is too far in the past")
	}
	decoded.Expiry = nil

	if decoded.IssuedAt.Time().After(time.Now().Add(1 * time.Second)) {
		t.Errorf("issued at is too far in the future")
	}
	if decoded.IssuedAt.Time().Before(time.Now().Add(-1 * time.Second)) {
		t.Errorf("issued at is too far in the past")
	}
	decoded.IssuedAt = nil

	if decoded.NotBefore.Time().After(time.Now().Add(1 * time.Second)) {
		t.Errorf("not before is too far in the future")
	}
	if decoded.NotBefore.Time().Before(time.Now().Add(-1 * time.Second)) {
		t.Errorf("not before is too far in the past")
	}
	decoded.NotBefore = nil

	expectedClaims := jwt.Claims{
		Subject:  "Kif Kroker",
		Audience: []string{"Zapp Brannigan"},
		ID:       "1",
		Issuer:   role + ".example.com",
	}

	if diff := deep.Equal(expectedClaims, decoded); diff != nil {
		t.Error(diff)
	}
}

type customToken struct {
	Foo string `json:"foo"`
}

func TestPrivateClaim(t *testing.T) {
	b, storage := getTestBackend(t)

	if _, err := writeConfig(b, storage, map[string]interface{}{"allowed_claims": []string{"aud", "foo"}}); err != nil {
		t.Fatalf("%v\n", err)
	}

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{"aud": "an audience"}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	claims := map[string]interface{}{
		"foo": "bar",
	}

	var decoded customToken
	if err := getSignedTokenWithClaims(b, storage, role, claims, map[string]interface{}{}, &decoded, nil); err != nil {
		t.Fatalf("%v\n", err)
	}

	expectedClaims := customToken{
		Foo: "bar",
	}

	if diff := deep.Equal(expectedClaims, decoded); diff != nil {
		t.Error(diff)
	}
}

func TestPrivateHeader(t *testing.T) {
	b, storage := getTestBackend(t)

	if _, err := writeConfig(b, storage, map[string]interface{}{"allowed_headers": []string{"tid"}}); err != nil {
		t.Fatalf("%v\n", err)
	}

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{"tid": "12345"}); err != nil {
		t.Fatalf("%v\n", err)
	}

	headers := map[string]interface{}{
		"tid": "12345",
	}

	decoded := map[string]interface{}{}
	if err := getSignedTokenWithClaims(b, storage, role, map[string]interface{}{}, headers, nil, decoded); err != nil {
		t.Fatalf("%v\n", err)
	}

	expectedHeaders := map[string]interface{}{
		"typ": "JWT",
		"tid": "12345",
	}

	if diff := deep.Equal(expectedHeaders, decoded); diff != nil {
		t.Error(diff)
	}
}

func TestAudienceAsArray(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	claims := map[string]interface{}{
		"aud": []interface{}{"foo", "bar"},
	}

	var decoded map[string]interface{}
	if err := getSignedTokenWithClaims(b, storage, role, claims, map[string]interface{}{}, &decoded, nil); err != nil {
		t.Fatalf("%v\n", err)
	}

	aud, ok := decoded["aud"].([]interface{})
	if !ok {
		t.Fatalf("audience is not a string array")
	}

	if diff := deep.Equal(aud, []interface{}{"foo", "bar"}); diff != nil {
		t.Error(diff)
	}
}

func TestRejectReservedClaims(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	data := map[string]interface{}{
		"claims": map[string]interface{}{
			"exp": 1234,
		},
	}

	req := &logical.Request{
		Operation:  logical.UpdateOperation,
		Path:       "sign/" + role,
		Storage:    *storage,
		Data:       data,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err == nil || resp != nil && !resp.IsError() {
		t.Fatalf("expected to get an error from sign. got:%v\n", resp)
	}
}

func TestRejectOverwriteRoleOtherClaim(t *testing.T) {
	b, storage := getTestBackend(t)

	role := "tester"

	if err := writeRole(b, storage, role, role+".example.com", map[string]interface{}{"aud": "an audience"}, map[string]interface{}{}); err != nil {
		t.Fatalf("%v\n", err)
	}

	data := map[string]interface{}{
		"claims": map[string]interface{}{
			"aud": 1234,
		},
	}

	req := &logical.Request{
		Operation:  logical.UpdateOperation,
		Path:       "sign/" + role,
		Storage:    *storage,
		Data:       data,
		MountPoint: "test",
	}

	resp, err := b.HandleRequest(context.Background(), req)
	if err == nil || resp != nil && !resp.IsError() {
		t.Fatalf("expected to get an error from sign. got:%v\n", resp)
	}
}
