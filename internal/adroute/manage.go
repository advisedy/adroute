package adroute

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

func newID() (string, error) {
	encoded, err := randomHex(16)
	if err != nil {
		return "", err
	}
	b, err := hex.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := fmt.Sprintf("%x", b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// ---- model groups -------------------------------------------------------

func (s *Store) CreateGroup(in GroupInput) (string, error) {
	if !validGroupName(in.Name) {
		return "", groupNameError()
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	err = s.mutate(func(st *State) error {
		if len(st.Groups) >= MaxGroups {
			return appError("模型组数量已达上限。", 400, "GROUP_LIMIT_REACHED")
		}
		for _, g := range st.Groups {
			if g.Name == in.Name {
				return appError("组的名称已存在，请换一个。", 409, "GROUP_NAME_CONFLICT")
			}
		}
		st.Groups = append(st.Groups, Group{ID: id, Name: in.Name})
		return nil
	})
	return id, err
}

func (s *Store) RenameGroup(id string, in GroupInput) error {
	if !validGroupName(in.Name) {
		return groupNameError()
	}
	return s.mutate(func(st *State) error {
		for i := range st.Groups {
			if st.Groups[i].ID != id {
				continue
			}
			for _, g := range st.Groups {
				if g.ID != id && g.Name == in.Name {
					return appError("组的名称已存在，请换一个。", 409, "GROUP_NAME_CONFLICT")
				}
			}
			st.Groups[i].Name = in.Name
			return nil
		}
		return appError("模型组不存在。", 404, "GROUP_NOT_FOUND")
	})
}

func (s *Store) DeleteGroup(id string) error {
	return s.mutate(func(st *State) error {
		index := -1
		for i := range st.Groups {
			if st.Groups[i].ID == id {
				index = i
			}
		}
		if index < 0 {
			return appError("模型组不存在。", 404, "GROUP_NOT_FOUND")
		}
		count := 0
		for _, m := range st.Models {
			if m.GroupID == id {
				count++
			}
		}
		if count > 0 {
			return appError("组内仍有模型，请先移动或删除这些模型。", 409, "GROUP_NOT_EMPTY")
		}
		st.Groups = append(st.Groups[:index], st.Groups[index+1:]...)
		return nil
	})
}

// ---- model entries ------------------------------------------------------

// CopyModel makes a separate entry from the current saved model. Naming and
// copying happen under the same store mutation so concurrent copies are unique.
func (s *Store) CopyModel(sourceID string) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	err = s.mutate(func(st *State) error {
		if len(st.Models) >= MaxModels {
			return appError("模型数量已达上限。", 400, "MODEL_LIMIT_REACHED")
		}
		var source *Model
		for i := range st.Models {
			if st.Models[i].ID == sourceID {
				source = &st.Models[i]
				break
			}
		}
		if source == nil {
			return appError("模型不存在。", 404, "MODEL_NOT_FOUND")
		}
		name := source.Name + "-copy"
		for n := 2; modelNameTaken(st, source.GroupID, name, ""); n++ {
			name = fmt.Sprintf("%s-copy-%d", source.Name, n)
		}
		if !validModelName(name) {
			return appError("复制后的模型名称超过 100 个字符，请先缩短原模型名称。", 400, "MODEL_NAME_INVALID")
		}
		copy := *source
		copy.ID, copy.Name = id, name
		st.Models = append(st.Models, copy)
		return nil
	})
	return id, err
}

func (s *Store) CreateModel(in ModelInput) (string, error) {
	base, err := NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return "", err
	}
	if !validModelName(in.Name) {
		return "", modelNameError()
	}
	if !validRealModel(in.Model) {
		return "", appError("请填写有效的上游真实模型 ID（最多 256 个字符，不含控制字符）。", 400, "MODEL_UPSTREAM_ID_INVALID")
	}
	key := strings.TrimSpace(in.APIKey)
	if !validSecret(key) {
		return "", appError("请填写有效的上游 API Key。", 400, "UPSTREAM_KEY_INVALID")
	}
	website, err := NormalizeWebsite(in.Website)
	if err != nil {
		return "", err
	}
	profile := in.Profile
	if profile == "" {
		profile = "none"
	}
	if profile != "none" && !validProfileRef(profile) {
		return "", appError("修复包版本格式无效。", 400, "INVALID_REQUEST")
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	err = s.mutate(func(st *State) error {
		if len(st.Models) >= MaxModels {
			return appError("模型数量已达上限。", 400, "MODEL_LIMIT_REACHED")
		}
		if !hasGroup(st, in.GroupID) {
			return appError("目标模型组不存在。", 404, "GROUP_NOT_FOUND")
		}
		if modelNameTaken(st, in.GroupID, in.Name, "") {
			return appError("同一组内已有同名模型，请换一个名称。", 409, "MODEL_NAME_CONFLICT")
		}
		if profile != "none" && !adapterInstalled(st, profile) {
			return appError("修复包版本不存在。", 400, "ADAPTER_NOT_FOUND")
		}
		disabled := false
		if in.Disabled != nil {
			disabled = *in.Disabled
		}
		exposureDisabled := in.ExposureDisabled != nil && *in.ExposureDisabled
		st.Models = append(st.Models, Model{ID: id, GroupID: in.GroupID, Name: in.Name, Model: in.Model, BaseURL: base, APIKey: key, Website: website, Disabled: disabled, ExposureDisabled: exposureDisabled, Profile: profile})
		return nil
	})
	return id, err
}

func (s *Store) UpdateModel(id string, in ModelInput) error {
	base, err := NormalizeBaseURL(in.BaseURL)
	if err != nil {
		return err
	}
	if !validModelName(in.Name) {
		return modelNameError()
	}
	if !validRealModel(in.Model) {
		return appError("请填写有效的上游真实模型 ID（最多 256 个字符，不含控制字符）。", 400, "MODEL_UPSTREAM_ID_INVALID")
	}
	profile := in.Profile
	if profile != "" && profile != "none" && !validProfileRef(profile) {
		return appError("修复包版本格式无效。", 400, "INVALID_REQUEST")
	}
	key := strings.TrimSpace(in.APIKey)
	if key != "" && !validSecret(key) {
		return appError("请填写有效的上游 API Key。", 400, "UPSTREAM_KEY_INVALID")
	}
	website, err := NormalizeWebsite(in.Website)
	if err != nil {
		return err
	}
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		if !hasGroup(st, in.GroupID) {
			return appError("目标模型组不存在。", 404, "GROUP_NOT_FOUND")
		}
		if modelNameTaken(st, in.GroupID, in.Name, id) {
			return appError("同一组内已有同名模型，请换一个名称。", 409, "MODEL_NAME_CONFLICT")
		}
		current := st.Models[index]
		next := current
		next.GroupID = in.GroupID
		next.Name = in.Name
		next.Model = in.Model
		next.BaseURL = base
		next.Website = website
		if key != "" {
			next.APIKey = key
		}
		if in.Disabled != nil {
			next.Disabled = *in.Disabled
		}
		if in.ExposureDisabled != nil {
			next.ExposureDisabled = *in.ExposureDisabled
		}
		if profile != "" {
			if profile != "none" && !adapterInstalled(st, profile) {
				return appError("修复包版本不存在。", 400, "ADAPTER_NOT_FOUND")
			}
			next.Profile = profile
		}
		st.Models[index] = next
		// Closing the current default clears it in the same transaction.
		if next.Disabled && st.DefaultModelID == next.ID {
			st.DefaultModelID = ""
		}
		return nil
	})
}

func (s *Store) DeleteModel(id string) error {
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		if names := aggregatesUsingModel(st, id); len(names) > 0 {
			return appError(fmt.Sprintf("模型仍被聚合 %s 引用，请先从聚合中移除或删除这些聚合。", strings.Join(names, "、")), 409, "MODEL_IN_USE")
		}
		st.Models = append(st.Models[:index], st.Models[index+1:]...)
		if st.DefaultModelID == id {
			st.DefaultModelID = ""
		}
		return nil
	})
}

// SetModelExposure changes only public access; internal use and the selected
// default are retained. Public default resolution enforces exposure separately.
func (s *Store) SetModelExposure(id string, enabled bool) error {
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		st.Models[index].ExposureDisabled = !enabled
		return nil
	})
}

// SetModelContextWindow publishes one model's context window to clients. Zero
// clears it, which makes every client fall back to its own default. This is a
// client-side hint only: adroute never truncates or rejects a request by it.
func (s *Store) SetModelContextWindow(id string, value int) error {
	if value != 0 && (value < MinContextWindow || value > MaxContextWindow) {
		return appError(fmt.Sprintf("上下文窗口必须在 %d 到 %d 之间，或留空表示不设置。", MinContextWindow, MaxContextWindow), 400, "CONTEXT_WINDOW_INVALID")
	}
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		st.Models[index].ContextWindow = value
		return nil
	})
}

func (s *Store) SetModelEnabled(id string, enabled bool) error {
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		st.Models[index].Disabled = !enabled
		if !enabled && st.DefaultModelID == id {
			st.DefaultModelID = ""
		}
		return nil
	})
}

// SetDefault selects one saved, open model or aggregate as the web default.
// An empty id clears the default. It never changes any open/closed status.
func (s *Store) SetDefault(id string) error {
	return s.mutate(func(st *State) error {
		if id == "" {
			st.DefaultModelID = ""
			return nil
		}
		index := modelIndex(st, id)
		if index >= 0 {
			if st.Models[index].Disabled {
				return appError("已关闭的模型不能设为默认。", 403, "MODEL_ROUTE_DISABLED")
			}
			st.DefaultModelID = id
			return nil
		}
		agg := findAggregate(*st, id)
		if agg == nil {
			return appError("模型条目或聚合不存在。", 404, "MODEL_NOT_FOUND")
		}
		if agg.Disabled {
			return appError("已关闭的聚合不能设为默认。", 403, "AGGREGATE_DISABLED")
		}
		st.DefaultModelID = id
		return nil
	})
}

// SetModelProfile binds or clears the per-entry compatibility repair package.
func (s *Store) SetModelProfile(id, profile string) error {
	if profile == "" {
		profile = "none"
	}
	if profile != "none" && !validProfileRef(profile) {
		return appError("修复包版本格式无效。", 400, "INVALID_REQUEST")
	}
	return s.mutate(func(st *State) error {
		index := modelIndex(st, id)
		if index < 0 {
			return appError("模型条目不存在。", 404, "MODEL_NOT_FOUND")
		}
		if profile != "none" && !adapterInstalled(st, profile) {
			return appError("修复包版本不存在。", 400, "ADAPTER_NOT_FOUND")
		}
		st.Models[index].Profile = profile
		return nil
	})
}

// ---- aggregates -----------------------------------------------------------

func aggregateNameTaken(st *State, name, exceptID string) bool {
	for _, a := range st.Aggregates {
		if a.ID != exceptID && a.Name == name {
			return true
		}
	}
	return false
}

func aggregateIndex(st *State, id string) int {
	for i := range st.Aggregates {
		if st.Aggregates[i].ID == id {
			return i
		}
	}
	return -1
}

func validateAggregateInput(st *State, in AggregateInput) (int, error) {
	if !validModelName(in.Name) {
		return 0, appError("聚合名称只能使用英文字母、数字、点、下划线和连字符，不含中文、空格或斜杠。", 400, "AGGREGATE_NAME_INVALID")
	}
	rounds := in.MaxRounds
	if rounds == 0 {
		rounds = DefaultMaxRounds
	}
	if rounds < 1 || rounds > MaxAggregateRounds {
		return 0, appError(fmt.Sprintf("重试轮数必须是 1~%d 之间的整数。", MaxAggregateRounds), 400, "AGGREGATE_ROUNDS_INVALID")
	}
	if len(in.Members) < 1 || len(in.Members) > MaxAggregateSize {
		return 0, appError(fmt.Sprintf("聚合成员必须是 1~%d 个模型条目。", MaxAggregateSize), 400, "AGGREGATE_MEMBERS_INVALID")
	}
	seen := map[string]bool{}
	for _, member := range in.Members {
		if seen[member] {
			return 0, appError("聚合成员不能重复。", 400, "AGGREGATE_MEMBERS_INVALID")
		}
		seen[member] = true
		if modelIndex(st, member) < 0 {
			return 0, appError("聚合成员必须是已保存的模型条目。", 404, "AGGREGATE_MEMBER_NOT_FOUND")
		}
	}
	return rounds, nil
}

func (s *Store) CreateAggregate(in AggregateInput) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	err = s.mutate(func(st *State) error {
		if len(st.Aggregates) >= MaxAggregates {
			return appError("聚合数量已达上限。", 400, "AGGREGATE_LIMIT_REACHED")
		}
		if aggregateNameTaken(st, in.Name, "") {
			return appError("已有同名聚合，请换一个名称。", 409, "AGGREGATE_NAME_CONFLICT")
		}
		rounds, e := validateAggregateInput(st, in)
		if e != nil {
			return e
		}
		disabled := false
		if in.Disabled != nil {
			disabled = *in.Disabled
		}
		members := append([]string(nil), in.Members...)
		exposureDisabled := in.ExposureDisabled != nil && *in.ExposureDisabled
		st.Aggregates = append(st.Aggregates, Aggregate{ID: id, Name: in.Name, Members: members, MaxRounds: rounds, Disabled: disabled, ExposureDisabled: exposureDisabled})
		return nil
	})
	return id, err
}

func (s *Store) UpdateAggregate(id string, in AggregateInput) error {
	return s.mutate(func(st *State) error {
		index := aggregateIndex(st, id)
		if index < 0 {
			return appError("聚合不存在。", 404, "AGGREGATE_NOT_FOUND")
		}
		if aggregateNameTaken(st, in.Name, id) {
			return appError("已有同名聚合，请换一个名称。", 409, "AGGREGATE_NAME_CONFLICT")
		}
		rounds, e := validateAggregateInput(st, in)
		if e != nil {
			return e
		}
		current := st.Aggregates[index]
		current.Name = in.Name
		current.Members = append([]string(nil), in.Members...)
		current.MaxRounds = rounds
		if in.Disabled != nil {
			current.Disabled = *in.Disabled
		}
		if in.ExposureDisabled != nil {
			current.ExposureDisabled = *in.ExposureDisabled
		}
		st.Aggregates[index] = current
		if current.Disabled && st.DefaultModelID == id {
			st.DefaultModelID = ""
		}
		return nil
	})
}

func (s *Store) DeleteAggregate(id string) error {
	return s.mutate(func(st *State) error {
		index := aggregateIndex(st, id)
		if index < 0 {
			return appError("聚合不存在。", 404, "AGGREGATE_NOT_FOUND")
		}
		st.Aggregates = append(st.Aggregates[:index], st.Aggregates[index+1:]...)
		if st.DefaultModelID == id {
			st.DefaultModelID = ""
		}
		return nil
	})
}

func (s *Store) SetAggregateExposure(id string, enabled bool) error {
	return s.mutate(func(st *State) error {
		index := aggregateIndex(st, id)
		if index < 0 {
			return appError("聚合不存在。", 404, "AGGREGATE_NOT_FOUND")
		}
		st.Aggregates[index].ExposureDisabled = !enabled
		return nil
	})
}

func (s *Store) SetAggregateEnabled(id string, enabled bool) error {
	return s.mutate(func(st *State) error {
		index := aggregateIndex(st, id)
		if index < 0 {
			return appError("聚合不存在。", 404, "AGGREGATE_NOT_FOUND")
		}
		st.Aggregates[index].Disabled = !enabled
		if !enabled && st.DefaultModelID == id {
			st.DefaultModelID = ""
		}
		return nil
	})
}

// aggregatesUsingModel returns the names of chains that still reference the
// model entry; deletion must stay explicit rather than silently thinning them.
func aggregatesUsingModel(st *State, modelID string) []string {
	names := []string{}
	for _, a := range st.Aggregates {
		for _, member := range a.Members {
			if member == modelID {
				names = append(names, a.Name)
				break
			}
		}
	}
	return names
}

// ---- helpers ------------------------------------------------------------

func hasGroup(st *State, id string) bool {
	for _, g := range st.Groups {
		if g.ID == id {
			return true
		}
	}
	return false
}

func groupNameOf(st State, id string) string {
	for _, g := range st.Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return ""
}

func modelIndex(st *State, id string) int {
	for i := range st.Models {
		if st.Models[i].ID == id {
			return i
		}
	}
	return -1
}

// modelNameTaken compares public model names exactly and case-sensitively,
// including entries that are currently closed.
func modelNameTaken(st *State, groupID, name, exceptID string) bool {
	for _, m := range st.Models {
		if m.ID == exceptID {
			continue
		}
		if m.GroupID == groupID && m.Name == name {
			return true
		}
	}
	return false
}

func adapterInstalled(st *State, ref string) bool {
	for _, raw := range st.Adapters {
		var p AdapterPackage
		if json.Unmarshal(raw, &p) == nil && PackageRef(p) == ref {
			return true
		}
	}
	return false
}

func groupNameError() error {
	return appError("组名只能使用小写英文字母、数字和单个连字符，且不能以连字符开头或结尾。", 400, "GROUP_NAME_INVALID")
}

func modelNameError() error {
	return appError("模型名称只能使用英文字母、数字、点、下划线和连字符，不含中文、空格或斜杠。", 400, "MODEL_NAME_INVALID")
}

func strictLabel(v string, max int) error {
	if strings.TrimSpace(v) == "" || len(utf16.Encode([]rune(v))) > max || strings.ContainsAny(v, "\x00\n\r\x7f") {
		return appError("invalid name", 400, "INVALID_REQUEST")
	}
	return nil
}

func validProfileRef(s string) bool {
	if s == "none" {
		return true
	}
	parts := strings.Split(s, "@")
	if len(parts) != 2 || !adapterIDRE.MatchString(parts[0]) {
		return false
	}
	return adapterVersionRE.MatchString(parts[1])
}
