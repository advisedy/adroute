package adroute

import "encoding/json"

type configBackup struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Config  State  `json:"config"`
}

type configImport struct {
	ExpectedRevision *int64       `json:"expectedRevision"`
	Backup           configBackup `json:"backup"`
}

func (s *Store) importConfig(input configImport) error {
	backup := input.Backup
	invalid := func() error {
		return appError("配置文件无效或版本不受支持，现有配置未改变。", 400, "INVALID_BACKUP")
	}
	if input.ExpectedRevision == nil || backup.Format != "adroute-config" || backup.Version != 1 || backup.Config.Groups == nil || backup.Config.Models == nil {
		return invalid()
	}
	if err := ValidateState(backup.Config); err != nil {
		return invalid()
	}
	for _, model := range backup.Config.Models {
		website, err := NormalizeWebsite(model.Website)
		if err != nil || website != model.Website {
			return invalid()
		}
	}
	for _, raw := range backup.Config.Adapters {
		var pack AdapterPackage
		if json.Unmarshal(raw, &pack) != nil {
			return invalid()
		}
		verification, err := VerifyPackage(pack)
		if err != nil || !verification.ReplayPassed {
			return invalid()
		}
	}
	return s.mutate(func(current *State) error {
		if current.Revision != *input.ExpectedRevision {
			return appError("配置已发生变化，请刷新后重新导入。", 409, "REVISION_CONFLICT")
		}
		revision, initialized := current.Revision, current.AdapterDirectoryInitialized
		*current = cloneState(backup.Config)
		current.Revision, current.AdapterDirectoryInitialized = revision, initialized
		return nil
	})
}
