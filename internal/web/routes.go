package web

// routes registers every route of the contract (internal/webapi documents
// each body). Each area lives in its own file and registers itself, so the
// areas can be built independently without touching this one:
//
//	routes_session.go  session, pairing, the event stream
//	routes_board.go    the rail
//	routes_card.go     a card: head, thread, live, answer, send, actions
//	routes_docs.go     a card's spec, diff, pull request and stats
//	routes_files.go    a card's worktree files, opened in the browser
//	routes_create.go   the new-card form
//	routes_goals.go    goals and stacks
//	routes_ingest.go   spec ingest and bug import
//	routes_agent.go    the board-level agent session
//	routes_system.go   doctor and the fleet's stats
//	routes_push.go     Web Push subscriptions
//
// Register authenticated routes with s.api and the few that answer without
// a cookie with s.public. Every write passes the same-origin check in
// Handler whichever it uses. A route not built yet answers notYet (501).
func (s *Server) routes() {
	s.assetRoutes()
	s.sessionRoutes()
	s.boardRoutes()
	s.cardRoutes()
	s.docsRoutes()
	s.fileRoutes()
	s.createRoutes()
	s.goalRoutes()
	s.ingestRoutes()
	s.agentRoutes()
	s.systemRoutes()
	s.pushRoutes()
}
