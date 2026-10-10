package threadfold

import (
	"testing"

	"github.com/morphis/gummi/internal/state"
)

func TestPublishLine(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		p    state.PublishPayload
		want string
	}{
		{state.PublishPayload{Act: "push", Pushed: sha, Repo: "me/widget"}, "you pushed 0123456 to me/widget"},
		{state.PublishPayload{Act: "create", Pushed: sha, Repo: "me/widget", Number: 512, Draft: true}, "you pushed 0123456 to me/widget and opened draft PR #512"},
		{state.PublishPayload{Act: "create", Number: 512}, "you opened PR #512"},
		{state.PublishPayload{Act: "update", Number: 512}, "you edited PR #512"},
		{state.PublishPayload{Act: "push", Pushed: sha, Number: 512, ToDraft: true}, "you pushed 0123456 and returned PR #512 to draft"},
		{state.PublishPayload{Act: "ready", Number: 512}, "you marked PR #512 ready for review"},
		{state.PublishPayload{Act: "draft", Number: 512}, "you returned PR #512 to draft"},
	} {
		if got := PublishLine(tc.p); got != tc.want {
			t.Errorf("PublishLine(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}
