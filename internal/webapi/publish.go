package webapi

// Publishing a card (DESIGN §22): the facts a person confirms before an act
// and what the act did. The facts are resolved, not intended — the server
// reads them again when the act runs and refuses it ("facts-changed") when
// they no longer digest as Fingerprint.

// PublishFacts is GET /api/cards/{id}/publish?act=push|create|update|ready|draft
// (and, for a create on a fork, &baseRepo= for the repository chosen).
// Error is set, and the rest mostly empty, when the act is refused.
type PublishFacts struct {
	Act         string        `json:"act"`
	Error       *PublishError `json:"error,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	Branch      string        `json:"branch,omitempty"`
	Tip         string        `json:"tip,omitempty"`
	TipSubject  string        `json:"tipSubject,omitempty"`
	Ahead       int           `json:"ahead,omitempty"`
	Base        string        `json:"base,omitempty"`
	Remote      string        `json:"remote,omitempty"`
	PushURL     string        `json:"pushUrl,omitempty"`
	Push        string        `json:"push,omitempty"`
	Head        string        `json:"head,omitempty"`
	// BaseRepo is the repository the PR opens in. BaseRepos, on a fork,
	// are the two it can open in; BaseRepo is then empty until a person
	// has chosen (the refusal "base-unchosen").
	BaseRepo  string   `json:"baseRepo,omitempty"`
	BaseRepos []string `json:"baseRepos,omitempty"`
	GH        string   `json:"gh,omitempty"`
	// Hook is a pre-push hook git will run with the person's credential;
	// the page asks for it to be acknowledged before the act.
	Hook string     `json:"hook,omitempty"`
	PR   *PublishPR `json:"pr,omitempty"`
	// Draft is the state a created PR opens in; DraftLocked says the
	// quality floor decides it (DraftWhy), not the person.
	Draft       bool   `json:"draft,omitempty"`
	DraftLocked bool   `json:"draftLocked,omitempty"`
	DraftWhy    string `json:"draftWhy,omitempty"`
	// ToDraft says this push returns a ready PR to draft first: its tip
	// is not verified, and a ready PR never gains unverified commits.
	ToDraft  bool     `json:"toDraft,omitempty"`
	Title    string   `json:"title,omitempty"`
	Body     string   `json:"body,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

// PublishPR is a linked pull request as GitHub has it now.
type PublishPR struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Draft   bool   `json:"draft,omitempty"`
	HeadSHA string `json:"headSha,omitempty"`
}

// PublishError is a typed refusal or failure: Code is the same word on
// every face and the CLI ("dirty", "not-verified", "facts-changed" …).
type PublishError struct {
	Code string `json:"code"`
	Text string `json:"text"`
	Fix  string `json:"fix,omitempty"`
	// PR is an open pull request a "pr-exists" names, to link instead.
	PR int `json:"pr,omitempty"`
}

// PublishRequest is POST /api/cards/{id}/publish: the act, the
// fingerprint of the facts the person read, and their words.
type PublishRequest struct {
	Act         string `json:"act"`
	Fingerprint string `json:"fingerprint"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
	// BaseRepo is the repository the facts were read for, when the person
	// chose one.
	BaseRepo string `json:"baseRepo,omitempty"`
}

// PublishResult is what an act did, or — Error set and nothing published
// past what it names — why it did not.
type PublishResult struct {
	Act    string        `json:"act"`
	Error  *PublishError `json:"error,omitempty"`
	Pushed string        `json:"pushed,omitempty"`
	URL    string        `json:"url,omitempty"`
	Number int           `json:"number,omitempty"`
	Draft  bool          `json:"draft,omitempty"`
	// ToDraft is a ready PR returned to draft ahead of an unverified push.
	ToDraft bool `json:"toDraft,omitempty"`
	// LinkError is a PR opened that could not be linked to the card.
	LinkError string `json:"linkError,omitempty"`
}

// PublishOffer is the PR tab's publish strip: whether publishing is set up
// here (Why says what is missing when it is not) and which acts fit the
// card now, in the order the strip offers them.
type PublishOffer struct {
	Available bool     `json:"available"`
	Why       string   `json:"why,omitempty"`
	Acts      []string `json:"acts,omitempty"`
	// Unpushed counts the card's commits GitHub's PR head does not have.
	Unpushed int  `json:"unpushed,omitempty"`
	Draft    bool `json:"draft,omitempty"`
}
