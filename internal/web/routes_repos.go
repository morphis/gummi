package web

import (
	"context"
	"net/http"

	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

func (s *Server) repoRoutes() {
	s.api("GET /api/repos", s.handleRepos)
	s.api("POST /api/repos/fetch", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.FetchRepos(ctx, req)
	}))
	s.api("POST /api/repos/fastforward", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.FastForwardRepo(ctx, req)
	}))
	s.api("POST /api/repos/branches/delete", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.DeleteRepoBranch(ctx, req)
	}))
	s.api("POST /api/repos/branches/upstream", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.SetRepoBranchUpstream(ctx, req)
	}))
	s.api("POST /api/repos/remotes/add", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.AddRepoRemote(ctx, req)
	}))
	s.api("POST /api/repos/remotes/rename", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.RenameRepoRemote(ctx, req)
	}))
	s.api("POST /api/repos/remotes/seturl", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.SetRepoRemoteURL(ctx, req)
	}))
	s.api("POST /api/repos/remotes/remove", s.repoWrite(func(ctx context.Context, req webapi.RepoRequest) (ui.WebOutcome, error) {
		return s.opt.Board.RemoveRepoRemote(ctx, req)
	}))
}

// handleRepos is GET /api/repos: every managed repository and its
// branches.
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := s.opt.Board.Repos(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// repoWrite reads a RepoRequest and answers with what the write did. The
// repository and the branch travel in the body: the default repository's
// name is empty and a branch name holds slashes, so neither is a path
// segment.
func (s *Server) repoWrite(write func(context.Context, webapi.RepoRequest) (ui.WebOutcome, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req webapi.RepoRequest
		if !readBody(w, r, &req) {
			return
		}
		out, err := write(r.Context(), req)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.answer(w, out)
	}
}
