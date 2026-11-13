package adroute

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ConfigVersion is the only configuration format this build reads or writes.
// There is no migration path from earlier provider/key layouts.
const ConfigVersion = 6

func decodeStrict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

// State is the whole persisted configuration: user-created model groups plus
// model entries that each own their upstream connection settings.
type State struct {
	Version                     int               `json:"version"`
	Revision                    int64             `json:"revision"`
	Groups                      []Group           `json:"groups"`
	Models                      []Model           `json:"models"`
	Aggregates                  []Aggregate       `json:"aggregates"`
	DefaultModelID              string            `json:"defaultModelId,omitempty"`
	Adapters                    []json.RawMessage `json:"adapters,omitempty"`
	AdapterDirectoryInitialized *bool             `json:"adapterDirectoryInitialized,omitempty"`
}

func (s *State) UnmarshalJSON(data []byte) error {
	var present map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return err
	}
	for _, field := range []string{"version", "revision", "groups", "models", "aggregates"} {
		if _, ok := present[field]; !ok {
			return fmt.Errorf("missing required field %s", field)
		}
	}
	for _, field := range []string{"defaultModelId", "adapters", "adapterDirectoryInitialized"} {
		if raw, ok := present[field]; ok && string(raw) == "null" {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	type plain State
	var v plain
	if err := decodeStrict(data, &v); err != nil {
		return err
	}
	*s = State(v)
	return nil
}

// Group is a user-created classification. It owns no URL, credential, or
// upstream identity.
type Group struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived,omitempty"`
}

func (v *Group) UnmarshalJSON(b []byte) error {
	type plain Group
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	*v = Group(x)
	return validateOrganizationJSON(b)
}

// Model is a single routable entry with its own upstream connection settings.
type Model struct {
	ID               string `json:"id"`
	GroupID          string `json:"groupId"`
	Name             string `json:"name"`
	Model            string `json:"model"`
	BaseURL          string `json:"baseUrl"`
	APIKey           string `json:"apiKey"`
	Website          string `json:"website,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
	ExposureDisabled bool   `json:"exposureDisabled,omitempty"`
	Favorite         bool   `json:"favorite,omitempty"`
	Archived         bool   `json:"archived,omitempty"`
	ContextWindow    int    `json:"contextWindow,omitempty"`
	Profile          string `json:"profile"`
}

func (v *Model) UnmarshalJSON(b []byte) error {
	type plain Model
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	*v = Model(x)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if raw, ok := fields["disabled"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("disabled must be a boolean")
		}
		var disabled bool
		if err := json.Unmarshal(raw, &disabled); err != nil {
			return fmt.Errorf("disabled must be a boolean")
		}
		v.Disabled = disabled
	}
	if raw, ok := fields["baseUrl"]; ok && string(raw) == "null" {
		return fmt.Errorf("baseUrl must be a string")
	}
	if raw, ok := fields["exposureDisabled"]; ok && string(raw) == "null" {
		return fmt.Errorf("exposureDisabled must be a boolean")
	}
	if raw, ok := fields["apiKey"]; ok && string(raw) == "null" {
		return fmt.Errorf("apiKey must be a string")
	}
	if raw, ok := fields["contextWindow"]; ok && string(raw) == "null" {
		return fmt.Errorf("contextWindow must be an integer")
	}
	return validateOrganizationJSON(b)
}

// GroupInput is the create/rename payload for a model group.
type GroupInput struct {
	Name string `json:"name"`
}

func (v *GroupInput) UnmarshalJSON(b []byte) error {
	type plain GroupInput
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	*v = GroupInput(x)
	return nil
}

// ModelInput is the create/update payload for one model entry. Create requires
// name, model, baseUrl, apiKey and groupId. Update keeps the stored apiKey when
// apiKey is omitted or blank, and keeps the stored open/closed status when
// disabled is omitted.
type ModelInput struct {
	GroupID          string `json:"groupId"`
	Name             string `json:"name"`
	Model            string `json:"model"`
	BaseURL          string `json:"baseUrl"`
	APIKey           string `json:"apiKey,omitempty"`
	Website          string `json:"website,omitempty"`
	Profile          string `json:"profile,omitempty"`
	Disabled         *bool  `json:"disabled,omitempty"`
	ExposureDisabled *bool  `json:"exposureDisabled,omitempty"`

	apiKeyProvided bool `json:"-"`
}

func (v *ModelInput) UnmarshalJSON(b []byte) error {
	type plain ModelInput
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(b, &fields); e != nil {
		return e
	}
	for _, field := range []string{"groupId", "name", "model", "baseUrl", "apiKey", "website", "profile"} {
		if raw, ok := fields[field]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must be a string", field)
		}
	}
	if raw, ok := fields["disabled"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("disabled must be a boolean")
		}
		var disabled bool
		if e := json.Unmarshal(raw, &disabled); e != nil {
			return fmt.Errorf("disabled must be a boolean")
		}
		x.Disabled = &disabled
	}
	if raw, ok := fields["apiKey"]; ok && string(raw) != "null" {
		x.apiKeyProvided = true
	}
	if raw, ok := fields["exposureDisabled"]; ok && string(raw) == "null" {
		return fmt.Errorf("exposureDisabled must be a boolean")
	}
	*v = ModelInput(x)
	return nil
}

// DiscoverInput asks the upstream at an unsaved base URL/credential for its
// model list. It never saves anything.
type DiscoverInput struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

func (v *DiscoverInput) UnmarshalJSON(b []byte) error {
	type plain DiscoverInput
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	*v = DiscoverInput(x)
	return nil
}

// Aggregate is an ordered failover chain over saved model entries. Members are
// stable model entry ids, so renames upstream never break the chain.
type Aggregate struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Members          []string `json:"members"`
	MaxRounds        int      `json:"maxRounds"`
	Disabled         bool     `json:"disabled,omitempty"`
	ExposureDisabled bool     `json:"exposureDisabled,omitempty"`
	Favorite         bool     `json:"favorite,omitempty"`
	Archived         bool     `json:"archived,omitempty"`
}

func (v *Aggregate) UnmarshalJSON(b []byte) error {
	type plain Aggregate
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	for _, field := range []string{"id", "name", "members", "maxRounds", "disabled", "exposureDisabled"} {
		if raw, ok := fields[field]; ok && string(raw) == "null" {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	*v = Aggregate(x)
	return validateOrganizationJSON(b)
}

// AggregateInput is the create/update payload for one failover chain. Create
// requires name and members; update keeps the stored open/closed status when
// disabled is omitted.
type AggregateInput struct {
	Name             string   `json:"name"`
	Members          []string `json:"members"`
	MaxRounds        int      `json:"maxRounds,omitempty"`
	Disabled         *bool    `json:"disabled,omitempty"`
	ExposureDisabled *bool    `json:"exposureDisabled,omitempty"`
}

func (v *AggregateInput) UnmarshalJSON(b []byte) error {
	type plain AggregateInput
	var x plain
	if e := decodeStrict(b, &x); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(b, &fields); e != nil {
		return e
	}
	for _, field := range []string{"name", "members"} {
		if raw, ok := fields[field]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must not be null", field)
		}
	}
	if raw, ok := fields["disabled"]; ok {
		if string(raw) == "null" {
			return fmt.Errorf("disabled must be a boolean")
		}
		var disabled bool
		if e := json.Unmarshal(raw, &disabled); e != nil {
			return fmt.Errorf("disabled must be a boolean")
		}
		x.Disabled = &disabled
	}
	*v = AggregateInput(x)
	if raw, ok := fields["exposureDisabled"]; ok && string(raw) == "null" {
		return fmt.Errorf("exposureDisabled must be a boolean")
	}
	return nil
}

// Route is the resolved upstream target for one public model id.
type Route struct {
	ID        string // stable model entry id, never user editable
	GroupName string
	ModelName string // public model name
	RouteID   string // adroute/<group>/<model name>
	Model     string // real upstream model id
	BaseURL   string
	APIKey    string
	Profile   string
	Disabled  bool
}

type Tokens struct {
	AdminToken     string `json:"adminToken"`
	ProxyToken     string `json:"proxyToken"`
	InstallationID string `json:"installationId"`
}
type Paths struct {
	Home    string
	Config  string
	Tokens  string
	Catalog string
	Run     string
	State   string
	Lock    string
	Logs    string
	Log     string
}

func cloneState(s State) State {
	out := s
	out.Groups = make([]Group, len(s.Groups))
	copy(out.Groups, s.Groups)
	out.Models = make([]Model, len(s.Models))
	copy(out.Models, s.Models)
	out.Aggregates = make([]Aggregate, len(s.Aggregates))
	for i, agg := range s.Aggregates {
		members := append([]string(nil), agg.Members...)
		agg.Members = members
		out.Aggregates[i] = agg
	}
	if s.Adapters != nil {
		out.Adapters = make([]json.RawMessage, len(s.Adapters))
		for i, a := range s.Adapters {
			out.Adapters[i] = append(json.RawMessage(nil), a...)
		}
	}
	if s.AdapterDirectoryInitialized != nil {
		v := *s.AdapterDirectoryInitialized
		out.AdapterDirectoryInitialized = &v
	}
	return out
}
