package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
)

// ErrNoWorkspaces is returned by the workspace API of a server started
// without -workspaces.
var ErrNoWorkspaces = errors.New("this server has no workspaces (start quill serve with -workspaces)")

// WorkspaceServer is the workspace API; Service implements it.
type WorkspaceServer interface {
	CreateWorkspace(ctx context.Context, spec workspace.Spec) (*workspace.Workspace, error)
	ListWorkspaces(ctx context.Context) ([]workspace.Workspace, error)
	GetWorkspace(ctx context.Context, id string) (*workspace.Workspace, error)
	StopWorkspace(ctx context.Context, id string) error
	DeleteWorkspace(ctx context.Context, id string) error
}

var _ WorkspaceServer = (*Service)(nil)

// checkOwner refuses a signed-in user another user's thread. Threads
// created without login have no owner and stay open to everyone.
func checkOwner(ctx context.Context, info *sessions.Info) error {
	u, ok := auth.UserFrom(ctx)
	if !ok || info == nil || info.Owner == "" || info.Owner == u.ID {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrForbidden, info.ThreadID)
}

func (s *Service) ownWorkspace(ctx context.Context, id string) (*workspace.Workspace, error) {
	if s.workspaces == nil {
		return nil, ErrNoWorkspaces
	}
	ws, err := s.workspaces.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if u, ok := auth.UserFrom(ctx); ok && ws.Owner != u.ID {
		return nil, fmt.Errorf("%w: %s", workspace.ErrForbidden, id)
	}
	return ws, nil
}

func (s *Service) acquireWorkspace(ctx context.Context, id string) (*workspace.Workspace, func(), error) {
	if _, err := s.ownWorkspace(ctx, id); err != nil {
		return nil, nil, err
	}
	return s.workspaces.Acquire(ctx, id)
}

func (s *Service) CreateWorkspace(ctx context.Context, spec workspace.Spec) (*workspace.Workspace, error) {
	if s.workspaces == nil {
		return nil, ErrNoWorkspaces
	}
	u, _ := auth.UserFrom(ctx)
	return s.workspaces.Create(ctx, u.ID, spec, u.GitHubToken)
}

func (s *Service) ListWorkspaces(ctx context.Context) ([]workspace.Workspace, error) {
	if s.workspaces == nil {
		return nil, ErrNoWorkspaces
	}
	return s.workspaces.List(ctx, auth.OwnerFrom(ctx))
}

func (s *Service) GetWorkspace(ctx context.Context, id string) (*workspace.Workspace, error) {
	return s.ownWorkspace(ctx, id)
}

func (s *Service) StopWorkspace(ctx context.Context, id string) error {
	if _, err := s.ownWorkspace(ctx, id); err != nil {
		return err
	}
	return s.workspaces.Stop(ctx, id)
}

// DeleteWorkspace removes the workspace; its threads stay listed but can no
// longer run.
func (s *Service) DeleteWorkspace(ctx context.Context, id string) error {
	if _, err := s.ownWorkspace(ctx, id); err != nil {
		return err
	}
	if err := s.workspaces.Delete(ctx, id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.agents {
		if k.workspace == id {
			delete(s.agents, k)
		}
	}
	return nil
}
