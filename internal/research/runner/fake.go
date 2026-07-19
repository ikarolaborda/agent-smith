package runner

import (
	"context"
	"sync"

	"github.com/ikarolaborda/agent-smith/internal/research/domain"
)

// FakeBackend is a deterministic test/evaluation backend.
type FakeBackend struct {
	AssuranceValue Assurance
	PreflightError error
	ExecuteFunc    func(context.Context, domain.WorkerJob, string) (Execution, error)

	mu    sync.Mutex
	Calls []domain.WorkerJob
}

func (f *FakeBackend) Preflight(context.Context) (Assurance, error) {
	if f.PreflightError != nil {
		return Assurance{}, f.PreflightError
	}
	if f.AssuranceValue.Isolation == "" {
		return Assurance{Backend: "fake", Isolation: "deterministic_test_double", Runtime: "fake"}, nil
	}
	return f.AssuranceValue, nil
}

func (f *FakeBackend) Execute(ctx context.Context, job domain.WorkerJob, staging string) (Execution, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, job)
	f.mu.Unlock()
	if f.ExecuteFunc != nil {
		return f.ExecuteFunc(ctx, job, staging)
	}
	return Execution{Status: domain.RunCompleted}, nil
}

func (f *FakeBackend) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
