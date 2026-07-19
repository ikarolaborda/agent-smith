package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/novelty"
	"github.com/ikarolaborda/agent-smith/internal/research/pipeline"
	"github.com/ikarolaborda/agent-smith/internal/research/runner"
	"github.com/ikarolaborda/agent-smith/internal/research/service"
	"github.com/ikarolaborda/agent-smith/internal/research/sourcefetch"
	"github.com/ikarolaborda/agent-smith/internal/research/store"
)

type principalContextKey struct{}

type credentialHash struct {
	digest    [sha256.Size]byte
	principal domain.Principal
}

type researchRuntime struct {
	store               *store.Store
	service             *service.Service
	workspaceRoots      []string
	credentials         []credentialHash
	broker              *runner.Broker
	noveltyBroker       *novelty.Broker
	noveltySources      map[string]novelty.Source
	sourceBroker        *sourcefetch.Broker
	sourceManifestKeyID string
}

func buildResearchRuntime(ctx context.Context, opts ResearchModeOptions) (*researchRuntime, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("server: research data directory required")
	}
	if len(opts.WorkspaceRoots) == 0 {
		return nil, errors.New("server: at least one research workspace root required")
	}
	if len(opts.Credentials) == 0 {
		return nil, errors.New("server: at least one research credential required")
	}
	runtime := &researchRuntime{}
	for _, root := range opts.WorkspaceRoots {
		canonical, err := canonicalDirectory(root)
		if err != nil {
			return nil, errors.New("server: invalid research workspace root: " + err.Error())
		}
		runtime.workspaceRoots = append(runtime.workspaceRoots, canonical)
	}
	seenPrincipals := map[string]bool{}
	for _, credential := range opts.Credentials {
		if len(credential.Token) < 32 {
			return nil, errors.New("server: research bearer tokens must contain at least 32 characters")
		}
		if credential.Principal.ID == "" || len(credential.Principal.Roles) == 0 {
			return nil, errors.New("server: research credential requires principal id and roles")
		}
		if seenPrincipals[credential.Principal.ID] {
			return nil, errors.New("server: duplicate research principal id")
		}
		seenPrincipals[credential.Principal.ID] = true
		runtime.credentials = append(runtime.credentials, credentialHash{digest: sha256.Sum256([]byte(credential.Token)), principal: credential.Principal})
	}
	repository, err := store.Open(ctx, store.Config{Root: opts.DataDir, MaxArtifactBytes: opts.MaxArtifactBytes, ArtifactEncryptionKeys: opts.ArtifactEncryptionKeys, ArtifactRetention: opts.ArtifactRetention})
	if err != nil {
		return nil, err
	}
	svc, err := service.New(repository, opts.MinimumReproductions)
	if err != nil {
		repository.Close()
		return nil, err
	}
	if err := svc.ConfigureWorkspaceRoots(runtime.workspaceRoots); err != nil {
		repository.Close()
		return nil, err
	}
	runtime.store, runtime.service = repository, svc
	if opts.SourceManifest != nil {
		doer := opts.SourceHTTPClient
		if doer == nil {
			doer = sourcefetch.NewHTTPClient()
		}
		acquisitionBroker, brokerErr := sourcefetch.NewBroker(doer, opts.SourceManifest.Sources(), 0, opts.SourceManifest.KeyID(), opts.SourceManifest.ExpiresAt())
		if brokerErr != nil {
			repository.Close()
			return nil, brokerErr
		}
		if err := svc.AttachSourceBroker(acquisitionBroker); err != nil {
			repository.Close()
			return nil, err
		}
		runtime.sourceBroker = acquisitionBroker
		runtime.sourceManifestKeyID = opts.SourceManifest.KeyID()
	}
	if len(opts.NoveltySources) > 0 {
		doer := opts.NoveltyHTTPClient
		if doer == nil {
			doer = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			}}
		}
		lookupBroker, brokerErr := novelty.NewBroker(doer, repository, opts.NoveltySources, 2<<20, 15*time.Minute)
		if brokerErr != nil {
			repository.Close()
			return nil, brokerErr
		}
		runtime.noveltyBroker = lookupBroker
		runtime.noveltySources = make(map[string]novelty.Source, len(opts.NoveltySources))
		for _, source := range opts.NoveltySources {
			runtime.noveltySources[source.Name] = source
		}
	}
	coordinator, err := pipeline.New(repository, opts.MinimumReproductions)
	if err != nil {
		repository.Close()
		return nil, err
	}
	if err := svc.ConfigureInternalRoot(pipeline.WorkRoot(repository.Root())); err != nil {
		repository.Close()
		return nil, err
	}
	if opts.RunnerBackend != nil {
		broker, err := runner.NewBroker(runner.Options{
			Backend: opts.RunnerBackend, Journal: repository, Artifacts: repository,
			StagingRoot: filepath.Join(repository.Root(), "staging"), GlobalConcurrency: opts.GlobalConcurrency,
			CampaignConcurrency: opts.CampaignConcurrency,
			OnResult:            coordinator.Ingest,
		})
		if err != nil {
			repository.Close()
			return nil, err
		}
		if err := broker.Start(ctx); err != nil {
			repository.Close()
			return nil, err
		}
		if err := svc.AttachBroker(broker); err != nil {
			broker.Close()
			repository.Close()
			return nil, err
		}
		runtime.broker = broker
	}
	return runtime, nil
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory: " + real)
	}
	return filepath.Clean(real), nil
}

func (s *Server) withAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static UI assets contain no research data and stay reachable so the
		// browser can collect a bearer token. Every /v1 API except health remains
		// authenticated in research mode.
		if s.research == nil || r.URL.Path == "/healthz" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-smith-research"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		candidate := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))))
		var principal domain.Principal
		matched := 0
		for _, credential := range s.research.credentials {
			equal := subtle.ConstantTimeCompare(candidate[:], credential.digest[:])
			if equal == 1 {
				principal = credential.principal
			}
			matched |= equal
		}
		if matched != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="agent-smith-research"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid bearer token required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func principalFromContext(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(domain.Principal)
	return principal, ok && principal.ID != ""
}

func principalHasAnyRole(principal domain.Principal, roles ...domain.Role) bool {
	for _, actual := range principal.Roles {
		if actual == domain.RoleAdmin {
			return true
		}
		for _, required := range roles {
			if actual == required {
				return true
			}
		}
	}
	return false
}

func (s *Server) workspaceAllowed(path string) bool {
	if s.research == nil {
		return true
	}
	candidate, err := canonicalDirectory(path)
	if err != nil {
		return false
	}
	for _, root := range s.research.workspaceRoots {
		relative, err := filepath.Rel(root, candidate)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
			return true
		}
	}
	return false
}
