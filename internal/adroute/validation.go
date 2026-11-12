package adroute

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

var identityRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
var adapterIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var adapterVersionRE = regexp.MustCompile(`^\d{1,5}\.\d{1,5}\.\d{1,5}$`)

// Group names are lowercase ASCII letters, digits, and single hyphens.
var groupNameRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Public model names are ASCII letters, digits, dot, underscore, and hyphen.
var modelNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const (
	MaxGroupNameLength = 48
	MaxModelNameLength = 100
	MaxRealModelLength = 256
	MaxGroups          = 100
	MaxModels          = 2000
	MaxAggregates      = 64
	MaxAggregateSize   = 8
	DefaultMaxRounds   = 5
	MaxAggregateRounds = 10
)

func ValidateState(s State) error {
	if s.Version != ConfigVersion || s.Revision < 0 || len(s.Groups) > MaxGroups || len(s.Models) > MaxModels || len(s.Aggregates) > MaxAggregates {
		return appError("unsupported configuration", 400, "INVALID_REQUEST")
	}
	if s.AdapterDirectoryInitialized != nil && *s.AdapterDirectoryInitialized != true && *s.AdapterDirectoryInitialized != false {
		return fmt.Errorf("invalid adapter directory status")
	}
	refs := map[string]bool{}
	if len(s.Adapters) > 32 {
		return fmt.Errorf("too many adapters")
	}
	total := 0
	for _, raw := range s.Adapters {
		total += len(raw)
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return fmt.Errorf("invalid adapter")
		}
		if err := adapterFields(obj); err != nil {
			return err
		}
		var id, version string
		if json.Unmarshal(obj["id"], &id) != nil || json.Unmarshal(obj["version"], &version) != nil {
			return fmt.Errorf("invalid adapter identity")
		}
		ref := id + "@" + version
		if !validProfileRef(ref) || refs[ref] {
			return fmt.Errorf("invalid or duplicate adapter")
		}
		refs[ref] = true
		if len(raw) > 256*1024 {
			return fmt.Errorf("adapter exceeds size limit")
		}
	}
	if total > 4*1024*1024 {
		return fmt.Errorf("adapter limit exceeded")
	}

	ids := map[string]bool{}
	groupNames := map[string]bool{}
	groupIDs := map[string]bool{}
	for _, g := range s.Groups {
		if !identityRE.MatchString(g.ID) || ids[g.ID] || !validGroupName(g.Name) || groupNames[g.Name] {
			return fmt.Errorf("invalid model group")
		}
		ids[g.ID] = true
		groupNames[g.Name] = true
		groupIDs[g.ID] = true
	}

	modelNames := map[string]bool{}
	modelIDs := map[string]bool{}
	for _, m := range s.Models {
		if !identityRE.MatchString(m.ID) || ids[m.ID] || modelIDs[m.ID] {
			return fmt.Errorf("invalid model entry")
		}
		if !groupIDs[m.GroupID] {
			return fmt.Errorf("model group not found")
		}
		if !validModelName(m.Name) {
			return fmt.Errorf("invalid model name")
		}
		key := m.GroupID + "\x00" + m.Name
		if modelNames[key] {
			return fmt.Errorf("duplicate model name in group")
		}
		modelNames[key] = true
		if !validRealModel(m.Model) {
			return fmt.Errorf("invalid upstream model id")
		}
		u, e := NormalizeBaseURL(m.BaseURL)
		if e != nil || u != m.BaseURL {
			return fmt.Errorf("invalid model base URL")
		}
		if !validSecret(m.APIKey) {
			return fmt.Errorf("invalid model credential")
		}
		if !(m.Profile == "none" || validProfileRef(m.Profile)) {
			return fmt.Errorf("invalid adapter profile")
		}
		if m.Profile != "none" && !refs[m.Profile] {
			return appError("adapter reference not found", 409, "ADAPTER_NOT_FOUND")
		}
		ids[m.ID] = true
		modelIDs[m.ID] = true
	}

	aggregateNames := map[string]bool{}
	for _, a := range s.Aggregates {
		if !identityRE.MatchString(a.ID) || ids[a.ID] {
			return fmt.Errorf("invalid aggregate entry")
		}
		if !validModelName(a.Name) || aggregateNames[a.Name] {
			return fmt.Errorf("invalid or duplicate aggregate name")
		}
		aggregateNames[a.Name] = true
		if len(a.Members) < 1 || len(a.Members) > MaxAggregateSize {
			return fmt.Errorf("aggregate member count out of range")
		}
		seen := map[string]bool{}
		for _, member := range a.Members {
			if !modelIDs[member] || seen[member] {
				return fmt.Errorf("invalid aggregate member")
			}
			seen[member] = true
		}
		if a.MaxRounds < 1 || a.MaxRounds > MaxAggregateRounds {
			return fmt.Errorf("invalid aggregate round count")
		}
		ids[a.ID] = true
	}

	if s.DefaultModelID != "" {
		if !identityRE.MatchString(s.DefaultModelID) {
			return fmt.Errorf("invalid default model identity")
		}
		found := false
		for _, m := range s.Models {
			if m.ID == s.DefaultModelID && !m.Disabled {
				found = true
			}
		}
		for _, a := range s.Aggregates {
			if a.ID == s.DefaultModelID && !a.Disabled {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing default model")
		}
	}
	return nil
}

func validGroupName(s string) bool {
	return len(s) <= MaxGroupNameLength && groupNameRE.MatchString(s)
}

func validModelName(s string) bool {
	return len(s) <= MaxModelNameLength && modelNameRE.MatchString(s)
}

func validRealModel(s string) bool {
	return validLabel(s, MaxRealModelLength)
}

func adapterFields(obj map[string]json.RawMessage) error {
	if !onlyFields(obj, "schemaVersion", "id", "version", "name", "description", "engine", "options", "fixtures") {
		return fmt.Errorf("unknown adapter field")
	}
	for _, key := range []string{"schemaVersion", "id", "version", "name", "description", "engine", "options", "fixtures"} {
		if _, ok := obj[key]; !ok {
			return fmt.Errorf("missing adapter field %s", key)
		}
	}
	var schema int
	var engine, id, version, name, description string
	if json.Unmarshal(obj["schemaVersion"], &schema) != nil || schema != 1 || json.Unmarshal(obj["engine"], &engine) != nil || (engine != "responses-tools-v1" && engine != "responses-tools-v2" && engine != "responses-tools-v3") {
		return fmt.Errorf("invalid adapter schema")
	}
	if json.Unmarshal(obj["id"], &id) != nil || !adapterIDRE.MatchString(id) || json.Unmarshal(obj["version"], &version) != nil || !adapterVersionRE.MatchString(version) || json.Unmarshal(obj["name"], &name) != nil || !adapterLabel(name, 100) || json.Unmarshal(obj["description"], &description) != nil || !adapterLabel(description, 2000) {
		return fmt.Errorf("invalid adapter metadata")
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(obj["options"], &options) != nil || !onlyFields(options, "lateName", "splitCalls", "neutralizeCursorInstructions") {
		return fmt.Errorf("invalid adapter options")
	}
	for _, key := range []string{"lateName", "splitCalls"} {
		var value bool
		if raw, ok := options[key]; !ok || json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("adapter option %s must be boolean", key)
		}
	}
	if raw, ok := options["neutralizeCursorInstructions"]; ok {
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("adapter option neutralizeCursorInstructions must be boolean")
		}
	}
	var fixtures []json.RawMessage
	if json.Unmarshal(obj["fixtures"], &fixtures) != nil || len(fixtures) < 1 || len(fixtures) > 16 {
		return fmt.Errorf("invalid adapter fixtures")
	}
	for _, raw := range fixtures {
		var fixture map[string]json.RawMessage
		if json.Unmarshal(raw, &fixture) != nil {
			return fmt.Errorf("invalid adapter fixture")
		}
		allowed := []string{"name", "events", "expectedCalls"}
		if engine == "responses-tools-v3" {
			allowed = append(allowed, "expectedTexts")
		}
		if !onlyFields(fixture, allowed...) {
			return fmt.Errorf("unknown adapter fixture field")
		}
		var fixtureName string
		if json.Unmarshal(fixture["name"], &fixtureName) != nil || !adapterLabel(fixtureName, 100) {
			return fmt.Errorf("invalid fixture name")
		}
		var events []json.RawMessage
		if json.Unmarshal(fixture["events"], &events) != nil || len(events) < 1 || len(events) > 256 {
			return fmt.Errorf("invalid fixture events")
		}
		for _, eventRaw := range events {
			var event map[string]json.RawMessage
			var eventType string
			if json.Unmarshal(eventRaw, &event) != nil || json.Unmarshal(event["type"], &eventType) != nil || eventType == "" {
				return fmt.Errorf("invalid fixture event")
			}
		}
		var calls []json.RawMessage
		if json.Unmarshal(fixture["expectedCalls"], &calls) != nil || len(calls) > 64 {
			return fmt.Errorf("invalid expected calls")
		}
		for _, callRaw := range calls {
			var call map[string]json.RawMessage
			if json.Unmarshal(callRaw, &call) != nil || !onlyFields(call, "call_id", "name", "arguments") {
				return fmt.Errorf("invalid adapter call")
			}
			for _, key := range []string{"call_id", "name", "arguments"} {
				var value string
				if json.Unmarshal(call[key], &value) != nil || !adapterLabel(value, 256*1024) {
					return fmt.Errorf("invalid adapter call field")
				}
			}
		}
		if engine == "responses-tools-v3" {
			var texts []string
			if json.Unmarshal(fixture["expectedTexts"], &texts) != nil {
				return fmt.Errorf("invalid expected texts")
			}
		}
	}
	return nil
}
func adapterLabel(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf16Length(s) <= max && !strings.ContainsAny(s, "\x00\x01\x02\x03\x04\x05\x06\x08\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f")
}
func onlyFields(m map[string]json.RawMessage, allowed ...string) bool {
	if m == nil {
		return false
	}
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range m {
		if !ok[k] {
			return false
		}
	}
	return true
}
func validLabel(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && utf16Length(s) <= max && !strings.ContainsAny(s, "\x00\x01\x02\x03\x04\x05\x06\a\b\t\n\v\f\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f\x7f")
}
func validSecret(s string) bool {
	return s != "" && utf16Length(s) <= 16384 && !strings.ContainsAny(s, "\x00\x01\x02\x03\x04\x05\x06\a\b\t\n\v\f\r\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f \x7f")
}
func utf16Length(s string) int { return len(utf16.Encode([]rune(s))) }
