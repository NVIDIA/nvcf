// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvcf/src/compute-plane-services/request-trace-uploader/transform/suppress"
)

const (
	// EnvSuppressNCAIDs lists suppressed NCA ids, separated by commas. It
	// builds one stripPayload rule, named NCAIDRuleName, on the headers in
	// EnvSuppressNCAHeaders. Unset or blank turns it off.
	EnvSuppressNCAIDs = "REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_IDS"
	// EnvSuppressNCAHeaders lists, separated by commas, the headers that
	// carry the NCA id, in the order they are read. The uploader has no
	// built-in list: it is required while EnvSuppressNCAIDs has an id.
	EnvSuppressNCAHeaders = "REQUEST_TRACE_UPLOADER_SUPPRESS_NCA_HEADERS"
	// EnvSuppressionRules holds further named rules as a JSON list of
	// {"name", "headers", "values", "action"} objects. They are evaluated
	// after the NCA id rule, in order.
	EnvSuppressionRules = "REQUEST_TRACE_UPLOADER_SUPPRESSION_RULES"
	// EnvSuppressHeaderless is the policy for records that carry no headers
	// while suppression is on: "export" (the default) or "drop".
	EnvSuppressHeaderless = "REQUEST_TRACE_UPLOADER_SUPPRESS_HEADERLESS"
	// NCAIDRuleName names the rule EnvSuppressNCAIDs builds.
	NCAIDRuleName = "nca_id"
)

// ruleNamePattern is the telemetry label value shape, so a rule name can
// label a counter.
var ruleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

type ruleSpec struct {
	Name    string   `json:"name"`
	Headers []string `json:"headers"`
	Values  []string `json:"values"`
	Action  string   `json:"action"`
}

// suppressionValue builds the suppression rules. Every problem is an error,
// never a fallback: a rule that silently turned off would export payloads
// an operator asked to suppress. Errors never echo a configured value,
// because values can be customer identifiers.
func suppressionValue(lookup LookupFunc) (suppress.Config, error) {
	var cfg suppress.Config
	headers, err := ncaHeaders(valueOrDefault(lookup, EnvSuppressNCAHeaders, ""))
	if err != nil {
		return suppress.Config{}, err
	}
	ids := suppress.FromIDs(strings.Split(valueOrDefault(lookup, EnvSuppressNCAIDs, ""), ","), headers...)
	if len(ids.Rules) > 0 && len(headers) == 0 {
		return suppress.Config{}, fmt.Errorf("%s is set, so %s must list the headers that carry the NCA id", EnvSuppressNCAIDs, EnvSuppressNCAHeaders)
	}
	for _, rule := range ids.Rules {
		rule.Name = NCAIDRuleName
		cfg.Rules = append(cfg.Rules, rule)
	}

	explicit, err := explicitRules(valueOrDefault(lookup, EnvSuppressionRules, ""))
	if err != nil {
		return suppress.Config{}, err
	}
	cfg.Rules = append(cfg.Rules, explicit...)

	switch policy := suppress.HeaderlessPolicy(strings.TrimSpace(valueOrDefault(lookup, EnvSuppressHeaderless, string(suppress.HeaderlessExport)))); policy {
	case suppress.HeaderlessExport, suppress.HeaderlessDrop:
		cfg.Headerless = policy
	default:
		return suppress.Config{}, fmt.Errorf("%s must be %q or %q", EnvSuppressHeaderless, suppress.HeaderlessExport, suppress.HeaderlessDrop)
	}

	if err := cfg.Validate(); err != nil {
		return suppress.Config{}, fmt.Errorf("%s or %s: %w", EnvSuppressNCAIDs, EnvSuppressionRules, err)
	}
	return cfg, nil
}

// ncaHeaders parses EnvSuppressNCAHeaders. Unset or blank is no list. Each
// name is trimmed and lower-cased, as header lookup ignores case; a blank or
// repeated name is an error. Errors give positions, not names.
func ncaHeaders(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	headers := make([]string, 0, len(parts))
	for i, part := range parts {
		header := strings.ToLower(strings.TrimSpace(part))
		if header == "" {
			return nil, fmt.Errorf("%s entry %d is blank", EnvSuppressNCAHeaders, i)
		}
		if j := slices.Index(headers, header); j >= 0 {
			return nil, fmt.Errorf("%s entry %d repeats entry %d", EnvSuppressNCAHeaders, i, j)
		}
		headers = append(headers, header)
	}
	return headers, nil
}

func explicitRules(raw string) ([]suppress.Rule, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var specs []ruleSpec
	if err := decoder.Decode(&specs); err != nil {
		return nil, fmt.Errorf("%s is not a JSON list of rules: %w", EnvSuppressionRules, err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s has data after the rule list", EnvSuppressionRules)
	}
	rules := make([]suppress.Rule, 0, len(specs))
	for i, spec := range specs {
		if !ruleNamePattern.MatchString(spec.Name) {
			return nil, fmt.Errorf("%s rule %d: name must match %s", EnvSuppressionRules, i, ruleNamePattern)
		}
		action := suppress.Action(spec.Action)
		if action != suppress.StripPayload && action != suppress.DropRecord {
			return nil, fmt.Errorf("%s rule %q: action must be %q or %q", EnvSuppressionRules, spec.Name, suppress.StripPayload, suppress.DropRecord)
		}
		headers := make([]string, len(spec.Headers))
		for j, header := range spec.Headers {
			// Header lookup ignores case, so the configured case does not
			// matter.
			headers[j] = strings.ToLower(strings.TrimSpace(header))
		}
		rules = append(rules, suppress.Rule{Name: spec.Name, Headers: headers, Values: spec.Values, Action: action})
	}
	return rules, nil
}
