package adroute

import "encoding/json"

type AdapterFileState struct {
	File    string `json:"file"`
	Ref     string `json:"ref,omitempty"`
	Status  string `json:"status"`
	Hash    string `json:"hash,omitempty"`
	Message string `json:"message,omitempty"`
}
type AdapterBinding struct {
	ModelID   string `json:"modelId"`
	RouteID   string `json:"routeId"`
	GroupName string `json:"groupName"`
	Model     string `json:"model"`
	ModelName string `json:"modelName"`
}
type AdapterDirectoryPackage struct {
	Ref          string           `json:"ref"`
	ID           string           `json:"id"`
	Version      string           `json:"version"`
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	Engine       string           `json:"engine"`
	Hash         string           `json:"hash"`
	Verified     bool             `json:"verified"`
	LiveVerified bool             `json:"liveVerified"`
	File         string           `json:"file"`
	SourceStatus string           `json:"sourceStatus"`
	CanApply     bool             `json:"canApply"`
	Bindings     []AdapterBinding `json:"bindings"`
}
type AdapterDirectoryState struct {
	Directory       string                    `json:"directory"`
	WorkDirectory   string                    `json:"workDirectory"`
	DirectoryStatus string                    `json:"directoryStatus"`
	DirectoryError  string                    `json:"directoryError"`
	Files           []AdapterFileState        `json:"files"`
	Packages        []AdapterDirectoryPackage `json:"packages"`
}

func (d *AdapterDirectory) stateLocked() AdapterDirectoryState {
	filesByName := map[string]AdapterFileState{}
	files := append([]AdapterFileState{}, d.files...)
	for _, f := range files {
		filesByName[f.File] = f
	}
	state := AdapterDirectoryState{Directory: d.Directory, WorkDirectory: d.WorkDirectory, DirectoryStatus: d.status, DirectoryError: d.directoryError, Files: files, Packages: []AdapterDirectoryPackage{}}
	for _, raw := range d.Store.Snapshot().Adapters {
		var p AdapterPackage
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		ref := PackageRef(p)
		file := ref + ".adroute-adapter.json"
		candidate, ok := filesByName[file]
		source := candidate.Status
		if !ok {
			if d.status == "unscanned" {
				source = "unscanned"
			} else {
				source = "missing"
			}
		}
		bindings := adapterBindings(d.Store.Snapshot(), ref)
		state.Packages = append(state.Packages, AdapterDirectoryPackage{Ref: ref, ID: p.ID, Version: p.Version, Name: p.Name, Description: p.Description, Engine: p.Engine, Hash: PackageHash(p), Verified: true, LiveVerified: false, File: file, SourceStatus: source, CanApply: d.status != "error" && (source == "unscanned" || source == "verified"), Bindings: bindings})
	}
	return state
}

func adapterBindings(st State, ref string) []AdapterBinding {
	out := []AdapterBinding{}
	for _, m := range st.Models {
		if m.Profile != ref {
			continue
		}
		r := routeFor(st, m)
		out = append(out, AdapterBinding{ModelID: m.ID, RouteID: r.RouteID, GroupName: r.GroupName, Model: m.Model, ModelName: m.Name})
	}
	return out
}
