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

package dynamodb

import (
	"strconv"
	"strings"
	"time"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// expr collects the attribute name and value placeholders of one request.
// DynamoDB rejects placeholders a request does not use, so every request
// builds its own.
type expr struct {
	names  map[string]string
	values map[string]ddbtypes.AttributeValue
}

// name returns the placeholder of an attribute name. Every attribute is
// referenced through a placeholder so that reserved words never matter.
func (e *expr) name(attr string) string {
	if e.names == nil {
		e.names = map[string]string{}
	}
	ph := "#" + attr
	e.names[ph] = attr
	return ph
}

// value returns the placeholder of a value. Placeholder names must be
// unique within one request.
func (e *expr) value(name string, v ddbtypes.AttributeValue) string {
	if e.values == nil {
		e.values = map[string]ddbtypes.AttributeValue{}
	}
	ph := ":" + name
	e.values[ph] = v
	return ph
}

// update builds an update expression from SET and REMOVE clauses.
type update struct {
	set    []string
	remove []string
}

func (u *update) setValue(e *expr, attr string, v ddbtypes.AttributeValue) {
	u.set = append(u.set, e.name(attr)+" = "+e.value(attr, v))
}

// setTime sets attr to t in Unix nanoseconds, or removes it for a zero t.
func (u *update) setTime(e *expr, attr string, t time.Time) {
	if n := unixNanos(t); n != nil {
		u.setValue(e, attr, numI(*n))
		return
	}
	u.remove = append(u.remove, e.name(attr))
}

// setExpiry sets expires_at, or leaves it alone when expiry is disabled.
func (u *update) setExpiry(e *expr, expiresAt int64) {
	if expiresAt != 0 {
		u.setValue(e, attrExpiresAt, numI(expiresAt))
	}
}

func (u *update) String() string {
	var b strings.Builder
	if len(u.set) > 0 {
		b.WriteString("SET ")
		b.WriteString(strings.Join(u.set, ", "))
	}
	if len(u.remove) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString("REMOVE ")
		b.WriteString(strings.Join(u.remove, ", "))
	}
	return b.String()
}

// Conditions.

func notExists(e *expr, attr string) string {
	return "attribute_not_exists(" + e.name(attr) + ")"
}

// genIs requires the stored generation the caller read.
func genIs(e *expr, gen uint64) string {
	return e.name(attrGen) + " = " + e.value("seen_gen", numU(gen))
}

// bodyIs requires the stored body the caller read, byte for byte.
func bodyIs(e *expr, body []byte) string {
	if len(body) == 0 {
		return notExists(e, attrBody)
	}
	return e.name(attrBody) + " = " + e.value("seen_body", bin(body))
}

// epochIs requires a lease epoch; 0 means no lease, which is stored as an
// absent attribute.
func epochIs(e *expr, epoch uint64) string {
	if epoch == 0 {
		return notExists(e, attrLeaseEpoch)
	}
	return e.name(attrLeaseEpoch) + " = " + e.value("epoch", numU(epoch))
}

// renewedIs requires a lease renewal time in Unix nanoseconds; nil means
// absent.
func renewedIs(e *expr, n *int64) string {
	if n == nil {
		return notExists(e, attrLeaseRenewedAt)
	}
	return e.name(attrLeaseRenewedAt) + " = " + e.value("seen_renewed_at", numI(*n))
}

// ownedBy allows a name reservation that is free or held by poolID.
func ownedBy(e *expr, poolID string) string {
	return notExists(e, attrPK) + " OR " + e.name(attrPoolID) + " = " + e.value("pool_id", str(poolID))
}

// Attribute values.

func str(s string) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberS{Value: s}
}

func numU(n uint64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatUint(n, 10)}
}

func numI(n int64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

func bin(b []byte) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberB{Value: b}
}

func (k key) attributes() map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{attrPK: str(k.pk), attrSK: str(k.sk)}
}

// itemSize estimates the DynamoDB size of an item: attribute names plus
// value lengths. It covers the S, N and B values this store writes.
func itemSize(av map[string]ddbtypes.AttributeValue) int {
	n := 0
	for name, v := range av {
		n += len(name)
		switch v := v.(type) {
		case *ddbtypes.AttributeValueMemberS:
			n += len(v.Value)
		case *ddbtypes.AttributeValueMemberN:
			n += len(v.Value)
		case *ddbtypes.AttributeValueMemberB:
			n += len(v.Value)
		default:
			n += 8
		}
	}
	return n
}
