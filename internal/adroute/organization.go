package adroute

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OrganizationInput is presentation metadata only, never routing policy.
// Pointer fields let independent controls update just their own flag.
type OrganizationInput struct {
	Favorite *bool `json:"favorite,omitempty"`
	Archived *bool `json:"archived,omitempty"`
}

func validateOrganizationJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"favorite", "archived"} {
		if raw, ok := fields[key]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must be a boolean", key)
		}
	}
	return nil
}

func (v *OrganizationInput) UnmarshalJSON(data []byte) error {
	type plain OrganizationInput
	var x plain
	if err := decodeStrict(data, &x); err != nil {
		return err
	}
	if err := validateOrganizationJSON(data); err != nil {
		return err
	}
	*v = OrganizationInput(x)
	return nil
}

// SetOrganization uses the same atomic persistence/revision boundary as edits.
// A group archive is inherited for display, preserving each child's own flag.
func (s *Store) SetOrganization(kind, id string, in OrganizationInput) error {
	if in.Favorite == nil && in.Archived == nil || kind == "groups" && in.Favorite != nil {
		return appError("请提供有效的收纳状态；分组只支持归档。", 400, "INVALID_REQUEST")
	}
	return s.mutate(func(st *State) error {
		switch kind {
		case "groups":
			for i := range st.Groups {
				if st.Groups[i].ID == id {
					st.Groups[i].Archived = *in.Archived
					return nil
				}
			}
			return appError("模型组不存在。", 404, "GROUP_NOT_FOUND")
		case "models":
			i := modelIndex(st, id)
			if i < 0 {
				return appError("模型不存在。", 404, "MODEL_NOT_FOUND")
			}
			if in.Favorite != nil {
				st.Models[i].Favorite = *in.Favorite
			}
			if in.Archived != nil {
				st.Models[i].Archived = *in.Archived
			}
		case "aggregates":
			i := aggregateIndex(st, id)
			if i < 0 {
				return appError("聚合不存在。", 404, "AGGREGATE_NOT_FOUND")
			}
			if in.Favorite != nil {
				st.Aggregates[i].Favorite = *in.Favorite
			}
			if in.Archived != nil {
				st.Aggregates[i].Archived = *in.Archived
			}
		default:
			return appError("收纳类型不存在。", 404, "NOT_FOUND")
		}
		return nil
	})
}
