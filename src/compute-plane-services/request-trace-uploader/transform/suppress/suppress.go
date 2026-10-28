// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package suppress applies record suppression rules.
//
// A rule names a header group, the values that match, and an action. Only
// records that carry a request payload are evaluated, because only those
// carry request headers and bodies. A header value is looked up and
// normalized exactly as the earlier Python uploader did, and stripPayload
// writes that uploader's stub byte for byte, so suppressed rows do not change
// shape for their consumers.
package suppress

import (
	"fmt"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform/compat"
)

// Action is what a matching rule does to a record.
type Action string

const (
	// StripPayload replaces the record with a stub that keeps its identity
	// and timing fields, drops every body and header, and records why.
	StripPayload Action = "stripPayload"
	// DropRecord removes the record from the export.
	DropRecord Action = "dropRecord"
)

// DefaultHeaders is the header group that carries the NCA id, in lookup
// order.
var DefaultHeaders = []string{"nvcf-ncaid", "nvcf-nca-id"}

// Rule matches records whose header group resolves to one of Values.
type Rule struct {
	// Name is the operator-assigned name that labels this rule's counters.
	// It must be unique in a Config. It never holds a header name or value.
	Name string
	// Headers lists lower-case header names. The first name with a
	// non-empty value decides.
	Headers []string
	// Values lists the ids that match, after NormalizeID.
	Values []string
	Action Action
}

// HeaderlessPolicy says what happens to records of a type that carries no
// request headers, so no rule can be evaluated on them. In Dynamo v1.4.0 that
// is every type except request_payload. It applies only while some rule is
// configured.
type HeaderlessPolicy string

const (
	// HeaderlessExport exports such records as they are. It is the default.
	HeaderlessExport HeaderlessPolicy = "export"
	// HeaderlessDrop removes such records from the export.
	HeaderlessDrop HeaderlessPolicy = "drop"
)

// Config is the ordered rule list. The first matching rule wins. No rules
// means suppression is off.
type Config struct {
	Rules []Rule
	// Headerless is the policy for records that carry no headers. Empty
	// means HeaderlessExport.
	Headerless HeaderlessPolicy
}

// FromIDs builds the configuration for a list of suppressed NCA ids: one
// stripPayload rule on DefaultHeaders. Blank ids are ignored, so an empty
// list turns suppression off.
func FromIDs(ids []string) Config {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		if normalized := NormalizeID(id); normalized != "" {
			values = append(values, normalized)
		}
	}
	if len(values) == 0 {
		return Config{}
	}
	return Config{Rules: []Rule{{Headers: DefaultHeaders, Values: values, Action: StripPayload}}}
}

// Validate rejects a rule with an unknown action, no header, a header that
// is blank or not lower case, no non-blank value, or a name another rule
// already has, and an unknown headerless policy.
func (c Config) Validate() error {
	switch c.Headerless {
	case "", HeaderlessExport, HeaderlessDrop:
	default:
		return fmt.Errorf("suppression: unknown headerless policy %q", c.Headerless)
	}
	names := make(map[string]bool, len(c.Rules))
	for i, rule := range c.Rules {
		if rule.Name != "" {
			if names[rule.Name] {
				return fmt.Errorf("suppression rule %d: name %q is used twice", i, rule.Name)
			}
			names[rule.Name] = true
		}
		switch rule.Action {
		case StripPayload, DropRecord:
		default:
			return fmt.Errorf("suppression rule %d: unknown action %q", i, rule.Action)
		}
		if len(rule.Headers) == 0 {
			return fmt.Errorf("suppression rule %d: no header", i)
		}
		for _, name := range rule.Headers {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("suppression rule %d: blank header name", i)
			}
			if name != strings.ToLower(name) {
				return fmt.Errorf("suppression rule %d: header name %q must be lower case", i, name)
			}
		}
		if len(normalizedSet(rule.Values)) == 0 {
			return fmt.Errorf("suppression rule %d: no value", i)
		}
	}
	return nil
}

func normalizedSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := NormalizeID(value); normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	return set
}

type compiledRule struct {
	name    string
	headers []string
	values  map[string]struct{}
	action  Action
}

// Engine applies a validated rule list.
type Engine struct {
	rules          []compiledRule
	dropHeaderless bool
}

// New validates cfg and builds an engine.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	engine := &Engine{dropHeaderless: cfg.Headerless == HeaderlessDrop}
	for _, rule := range cfg.Rules {
		engine.rules = append(engine.rules, compiledRule{
			name:    rule.Name,
			headers: append([]string(nil), rule.Headers...),
			values:  normalizedSet(rule.Values),
			action:  rule.Action,
		})
	}
	return engine, nil
}

// Active reports whether any rule is configured.
func (e *Engine) Active() bool { return len(e.rules) > 0 }

// Outcome reports what Apply did to one record.
type Outcome struct {
	// Evaluated reports that the record carries a request payload, so the
	// rules were checked.
	Evaluated bool
	// HeaderPresent reports that some rule's header group had a value. A
	// deployment where this never happens cannot suppress anything.
	HeaderPresent bool
	// Action is the action taken, or empty when no rule matched.
	Action Action
	// ID is the normalized header value that matched.
	ID string
	// Rule is the name of the rule that matched.
	Rule string
	// Present lists, in rule order, the names of the rules whose header
	// group had a value on this record, whether or not the value matched.
	Present []string
	// Headerless reports that the record carries no headers and the
	// headerless policy decided Action.
	Headerless bool
}

// Apply evaluates record and returns what to export: record itself when no
// rule matches, the stub for StripPayload, and nil for DropRecord. record is
// never changed. Every rule's header group is checked, so Outcome.Present is
// complete even after an earlier rule matched.
func (e *Engine) Apply(record *compat.Value) (*compat.Value, Outcome) {
	var outcome Outcome
	if len(e.rules) == 0 {
		return record, outcome
	}
	event := EventOf(record)
	if !CarriesPayload(event) {
		if e.dropHeaderless {
			outcome.Action = DropRecord
			outcome.Headerless = true
			return nil, outcome
		}
		return record, outcome
	}
	outcome.Evaluated = true
	sets := HeaderSets(event)
	var matched *compiledRule
	for i := range e.rules {
		rule := &e.rules[i]
		id := NormalizeID(ResolveHeader(sets, rule.headers...))
		if id == "" {
			continue
		}
		outcome.HeaderPresent = true
		if rule.name != "" {
			outcome.Present = append(outcome.Present, rule.name)
		}
		if matched != nil {
			continue
		}
		if _, ok := rule.values[id]; ok {
			matched = rule
			outcome.Action = rule.action
			outcome.ID = id
			outcome.Rule = rule.name
		}
	}
	switch {
	case matched == nil:
		return record, outcome
	case matched.action == DropRecord:
		return nil, outcome
	default:
		return stub(record, event, outcome.ID), outcome
	}
}

// EventOf returns the event of a record: the "event" member of a wrapped
// record, the record itself when bare, and nil when the record is not an
// object.
func EventOf(record *compat.Value) *compat.Value {
	if record == nil || record.Kind() != compat.Object {
		return nil
	}
	if record.Has("event") {
		return record.Get("event")
	}
	return record
}

// CarriesPayload reports whether event is a request_payload record.
func CarriesPayload(event *compat.Value) bool {
	if event == nil || event.Kind() != compat.Object || !event.Has("event_type") {
		return false
	}
	return compat.PyStr(event.Get("event_type")) == "request_payload"
}

// HeaderSets returns the header objects of an event in lookup order: the
// top-level "headers", then "payload.http_request_headers".
func HeaderSets(event *compat.Value) []*compat.Value {
	if event == nil || event.Kind() != compat.Object {
		return nil
	}
	var sets []*compat.Value
	if headers := event.Get("headers"); headers != nil && headers.Kind() == compat.Object {
		sets = append(sets, headers)
	}
	if payload := event.Get("payload"); payload != nil && payload.Kind() == compat.Object {
		if headers := payload.Get("http_request_headers"); headers != nil && headers.Kind() == compat.Object {
			sets = append(sets, headers)
		}
	}
	return sets
}

// ResolveHeader returns the first non-blank value for names across sets. In
// each set, each name takes the first key that lower-cases to it; a list
// value gives its first truthy item; the value is Python's str() of it,
// stripped.
func ResolveHeader(sets []*compat.Value, names ...string) string {
	for _, headers := range sets {
		for _, name := range names {
			value := firstKey(headers, name)
			if value == nil {
				continue
			}
			if value.Kind() == compat.Array {
				value = firstTruthy(value.Items())
				if value == nil {
					continue
				}
			}
			if !compat.Truthy(value) {
				continue
			}
			if text := compat.PyStrip(compat.PyStr(value)); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstKey(headers *compat.Value, name string) *compat.Value {
	for _, m := range headers.Members() {
		if lowerEquals(m.Key, name) {
			return m.Value
		}
	}
	return nil
}

func firstTruthy(items []*compat.Value) *compat.Value {
	for _, item := range items {
		if compat.Truthy(item) {
			return item
		}
	}
	return nil
}

// lowerEquals reports whether Python's key.lower() equals the ASCII name.
// Only ASCII capitals and the Kelvin sign lower-case to ASCII letters, so
// any other non-ASCII rune cannot match.
func lowerEquals(key, name string) bool {
	i := 0
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
			r += 'a' - 'A'
		case r == '\u212a':
			r = 'k'
		case r >= 0x80:
			return false
		}
		if i >= len(name) || name[i] != byte(r) {
			return false
		}
		i++
	}
	return i == len(name)
}

// NormalizeID strips white space and, when both are present, the "nca-"
// prefix and "-nca" suffix, so "nca-abc-nca" and "abc" match.
func NormalizeID(value string) string {
	value = compat.PyStrip(value)
	if !strings.HasPrefix(value, "nca-") || !strings.HasSuffix(value, "-nca") {
		return value
	}
	// The affixes can overlap, as in "nca-nca"; Python's slice is then empty.
	if len(value) < len("nca--nca") {
		return ""
	}
	return value[len("nca-") : len(value)-len("-nca")]
}

// stub builds the record that replaces a suppressed one. Copied keys keep
// this order and appear only when present; a wrapped record keeps only its
// timestamp.
func stub(record, event *compat.Value, id string) *compat.Value {
	var safeEvent *compat.Value
	if payload := event.Get("payload"); payload != nil && payload.Kind() == compat.Object {
		safePayload := copyKeys(payload, "request_id", "endpoint", "model")
		safePayload.Set("payload_complete", compat.NewBool(false))
		safePayload.Set("payload_drop_reason", compat.NewString("nca_id"))
		safePayload.Set("payload_suppressed", compat.NewBool(true))
		safePayload.Set("suppression_reason", compat.NewString("nca_id"))
		safePayload.Set("nca_id", compat.NewString(id))
		safePayload.Set("http_request_headers", singleHeader(id))
		safeEvent = copyKeys(event, "schema", "event_type", "event_time_unix_ms", "event_source")
		safeEvent.Set("payload", safePayload)
	} else {
		safeEvent = copyKeys(event, "schema", "schema_version", "event_type", "event_time_unix_ms",
			"request_id", "requested_streaming", "model")
		safeEvent.Set("payload_suppressed", compat.NewBool(true))
		safeEvent.Set("suppression_reason", compat.NewString("nca_id"))
		safeEvent.Set("nca_id", compat.NewString(id))
		safeEvent.Set("headers", singleHeader(id))
	}
	if record == event {
		return safeEvent
	}
	safeRecord := copyKeys(record, "timestamp")
	safeRecord.Set("event", safeEvent)
	return safeRecord
}

func copyKeys(from *compat.Value, keys ...string) *compat.Value {
	out := compat.NewObject()
	for _, key := range keys {
		if from.Has(key) {
			out.Set(key, from.Get(key))
		}
	}
	return out
}

func singleHeader(id string) *compat.Value {
	headers := compat.NewObject()
	headers.Set("nvcf-ncaid", compat.NewString(id))
	return headers
}
