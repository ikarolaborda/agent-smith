package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
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
		if s.research != nil {
			principal, ok := principalFromContext(r.Context())
			if !ok || !principalHasAnyRole(principal, domain.RoleOperator) {
				writeError(w, http.StatusForbidden, "forbidden", "operator role required to change the research workspace")
				return
			}
		}
		var req struct {
			Path string `json:"path"`
		}
		if !decodeJSONRequest(w, r, &req, maxControlBodyBytes) {
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
		if !s.workspaceAllowed(abs) {
			writeError(w, http.StatusForbidden, "workspace_out_of_scope", "folder is outside the operator-configured research workspace roots")
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
workspaceStatePath is where the active workspace folder is persisted so it
survives a reload or a server restart (the web UI restores it via GET
/v1/workspace). It is empty — disabling persistence — when no StateDir was
configured, which is the case in tests so they never touch the real home dir.
*/
func (s *Server) workspaceStatePath() string {
	if s.stateDir == "" {
		return ""
	}
	return filepath.Join(s.stateDir, "workspace")
}

/* persistWorkspace records (or clears) the active workspace on disk, best-effort. */
func (s *Server) persistWorkspace(dir string) {
	path := s.workspaceStatePath()
	if path == "" {
		return
	}
	if dir == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.logger.Warn("workspace: cannot persist folder", "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(dir), 0o600); err != nil {
		s.logger.Warn("workspace: cannot persist folder", "err", err)
	}
}

/*
restoreWorkspace reopens a previously-persisted workspace on startup so a reload
or restart keeps the folder. An explicit --workspace flag wins. A stale path
(deleted, not a directory, or outside the research roots) is ignored and cleared.
*/
func (s *Server) restoreWorkspace() {
	if s.getWorkspace() != "" {
		return
	}
	path := s.workspaceStatePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	dir := strings.TrimSpace(string(raw))
	if dir == "" {
		return
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() || !s.workspaceAllowed(dir) {
		_ = os.Remove(path)
		return
	}
	s.workspaceMu.Lock()
	s.workspace = dir
	s.workspaceMu.Unlock()
	s.logger.Info("workspace: restored from previous session", "root", dir)
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
	resp.Entries, resp.Truncated = walkWorkspaceTree(root, workspaceTreeMaxEntries)
	writeJSON(w, http.StatusOK, resp)
}

/*
walkWorkspaceTree returns a bounded, noise-filtered, sorted listing of root,
skipping the same directories as the tree endpoint and stopping at max entries.
It resolves a symlinked root so the walk matches what the file tools see. Shared
by the /workspace/tree endpoint and the prompt-grounding listing.
*/
func walkWorkspaceTree(root string, max int) ([]workspaceTreeEntry, bool) {
	entries := []workspaceTreeEntry{}
	truncated := false
	walkRoot := root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		walkRoot = real
	}
	_ = filepath.WalkDir(walkRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || p == walkRoot {
			return nil
		}
		if d.IsDir() && workspaceSkip[d.Name()] {
			return filepath.SkipDir
		}
		if len(entries) >= max {
			truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(walkRoot, p)
		if err != nil {
			return nil
		}
		entries = append(entries, workspaceTreeEntry{Path: rel, Dir: d.IsDir()})
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, truncated
}

/*
	workspaceListingMaxEntries bounds the auto-injected prompt listing (kept smaller

than the API tree so it never dominates the context window).
*/
const workspaceListingMaxEntries = 300

/*
buildWorkspaceListing renders a compact, deterministic file tree of the open
workspace for injection into the prompt, so the model always has ground truth of
what the folder contains and never disclaims access or invents files. It lists
paths only — never contents — so it stays small; the model reads specific files
via file_read/read_dir. Empty when no workspace is open or it is empty.
*/
func buildWorkspaceListing(root string) string {
	if root == "" {
		return ""
	}
	entries, truncated := walkWorkspaceTree(root, workspaceListingMaxEntries)
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Open workspace contents (ground truth — these files exist and are readable with file_read/read_dir):\n")
	for _, e := range entries {
		if e.Dir {
			b.WriteString(e.Path + "/\n")
			continue
		}
		b.WriteString(e.Path + "\n")
	}
	if truncated {
		b.WriteString("… (listing truncated; use read_dir on a subpath for the rest)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
