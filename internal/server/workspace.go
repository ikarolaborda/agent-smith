package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

/*
workspaceTreeMaxEntries bounds how many files/dirs the tree endpoint returns so
opening a huge folder cannot blow up the response or the UI.
*/
const workspaceTreeMaxEntries = 2000

/*
workspaceSkip is the set of directory names the tree never descends into, so the
listing stays focused on project source rather than build/VCS noise. It mirrors
the read_dir tool's skip set.
*/
var workspaceSkip = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".agent": true,
	"dist": true, "build": true, "target": true, "__pycache__": true,
	".idea": true, ".vscode": true, ".serena": true, ".next": true,
}

type workspaceState struct {
	Workspace string `json:"workspace"`
	Writable  bool   `json:"writable"`
}

type workspaceTreeEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
}

type workspaceTreeResponse struct {
	Workspace string               `json:"workspace"`
	Entries   []workspaceTreeEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

/*
handleWorkspace gets or sets the folder the agentic file_write/file_edit tools
operate on. GET reports the current folder; POST {"path": "..."} opens a folder
(absolute, must exist and be a directory) or, with an empty path, clears it back
to the read-only default. Setting a workspace is what makes file mutation
available from the web UI, mirroring an IDE's "open folder".
*/
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		ws := s.getWorkspace()
		writeJSON(w, http.StatusOK, workspaceState{Workspace: ws, Writable: ws != ""})
		return
	case http.MethodPost:
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body: "+err.Error())
			return
		}
		path := strings.TrimSpace(req.Path)
		if path == "" {
			s.setWorkspace("")
			writeJSON(w, http.StatusOK, workspaceState{Workspace: "", Writable: false})
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "cannot resolve path: "+err.Error())
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "folder not found: "+abs)
			return
		}
		if !info.IsDir() {
			writeError(w, http.StatusBadRequest, "invalid_request", "not a directory: "+abs)
			return
		}
		s.setWorkspace(abs)
		s.logger.Info("workspace: opened from web UI", "root", abs)
		writeJSON(w, http.StatusOK, workspaceState{Workspace: abs, Writable: true})
		return
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or POST required")
		return
	}
}

/*
handleWorkspaceTree returns a bounded, noise-filtered listing of the current
workspace so the UI can show the folder being worked on. It is read-only and
returns an empty list when no workspace is open.
*/
func (s *Server) handleWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	root := s.getWorkspace()
	resp := workspaceTreeResponse{Workspace: root, Entries: []workspaceTreeEntry{}}
	if root == "" {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	/*
		Walk the real path: filepath.WalkDir lstats its argument and won't descend
		when the root itself is a symlink (e.g. /tmp -> /private/tmp on macOS), so
		a symlinked workspace would list nothing. Resolving the root keeps the tree
		consistent with the file tools, which already operate through the symlink.
		Relative paths are still reported against the displayed root.
	*/
	walkRoot := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		walkRoot = real
	}

	_ = filepath.WalkDir(walkRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if p == walkRoot {
			return nil
		}
		if d.IsDir() && workspaceSkip[d.Name()] {
			return filepath.SkipDir
		}
		if len(resp.Entries) >= workspaceTreeMaxEntries {
			resp.Truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(walkRoot, p)
		if err != nil {
			return nil
		}
		resp.Entries = append(resp.Entries, workspaceTreeEntry{Path: rel, Dir: d.IsDir()})
		return nil
	})

	sort.Slice(resp.Entries, func(i, j int) bool { return resp.Entries[i].Path < resp.Entries[j].Path })
	writeJSON(w, http.StatusOK, resp)
}
