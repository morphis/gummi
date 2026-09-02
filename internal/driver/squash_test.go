package driver

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// fakeOriginMain points refs/remotes/origin/main at sha in root, so a
// harness repo with no real remote can still exercise Squash's
// no-dependency collapse-base arm (ResolveCollapseBase resolves to the
// branch's fork point with main).
func fakeOriginMain(t *testing.T, root, sha string) {
	t.Helper()
	if out, err := exec.CommandContext(context.Background(), "git", "-C", root, "update-ref", "refs/remotes/origin/main", sha).CombinedOutput(); err != nil {
		t.Fatalf("update-ref origin/main: %v\n%s", err, out)
	}
}

// TestDriverSquashCollapsesVerifiedBranch proves the happy path: a verified
// card's checkpoint-laden branch collapses to one commit off its fork point
// with main, main is untouched, and a `squashed` event carries the
// before/after/base shas and the message subject.
func TestDriverSquashCollapsesVerifiedBranch(t *testing.T) {
	h, d, id := driveVerified(t)
	fakeOriginMain(t, h.root, gitHead(t, h.root))
	before := gitHead(t, h.root)

	out, err := d.Squash(context.Background(), id, "feat(export): collapsed for review")
	if err != nil {
		t.Fatalf("Squash: %v", err)
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by squash: %s -> %s", before, got)
	}

	sq := lastEvent(h, "squashed")
	if sq == nil {
		t.Fatalf("no squashed event in stream; got %v", h.eventKinds())
	}
	f, _ := h.store.GetFeature(context.Background(), id)
	if sq["branch"] != f.BranchName() {
		t.Fatalf("squashed.branch = %v, want %s", sq["branch"], f.BranchName())
	}
	if sq["base_sha"] != before {
		t.Fatalf("squashed.base_sha = %v, want fork point %s", sq["base_sha"], before)
	}
	after, _ := sq["after_sha"].(string)
	if after == "" || after == before {
		t.Fatalf("squashed.after_sha = %q, want a fresh collapsed sha", after)
	}
	if sq["message_subject"] != "feat(export): collapsed for review" {
		t.Fatalf("squashed.message_subject = %v", sq["message_subject"])
	}
}

// TestDriverSquashRefusesDoneCard refuses a done card before any git
// mutation.
func TestDriverSquashRefusesDoneCard(t *testing.T) {
	h, d, id := driveVerified(t)
	if _, err := d.Merge(context.Background(), id, "feat(export): land it"); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	before := gitHead(t, h.root)

	out, err := d.Squash(context.Background(), id, "feat(export): collapsed")
	if err == nil {
		t.Fatal("Squash accepted a done card")
	}
	if !strings.Contains(err.Error(), "done") {
		t.Fatalf("error %q does not name the done refusal", err)
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by refused squash: %s -> %s", before, got)
	}
}

// TestDriverSquashRefusesLandedCard refuses a card whose branch actually
// landed on main, even if its card record has not yet transitioned to done
// (e.g. a TUI merge with thenDone unset leaves a card at verify with its
// content landed).
func TestDriverSquashRefusesLandedCard(t *testing.T) {
	h, d, id := driveVerified(t)
	f, err := h.store.GetFeature(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// land the branch through the real squash-merge path (which records the
	// landed sha Landed checks ancestry against), without going through
	// Driver.Merge, so the card stays parked at StageVerify.
	wt, err := d.eng.WorktreesFor(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wt.SquashMerge(context.Background(), &f, "feat(export): land it directly"); err != nil {
		t.Fatal(err)
	}
	before := gitHead(t, h.root)

	out, err := d.Squash(context.Background(), id, "feat(export): collapsed")
	if err == nil {
		t.Fatal("Squash accepted an already-landed card")
	}
	if !strings.Contains(err.Error(), "landed") {
		t.Fatalf("error %q does not name the landed refusal", err)
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by refused squash: %s -> %s", before, got)
	}
}

// TestDriverSquashInvalidMessageRefused refuses a non-Conventional-Commits
// message before any git mutation.
func TestDriverSquashInvalidMessageRefused(t *testing.T) {
	h, d, id := driveVerified(t)
	fakeOriginMain(t, h.root, gitHead(t, h.root))
	before := gitHead(t, h.root)

	out, err := d.Squash(context.Background(), id, "not a conventional commit subject")
	if err == nil {
		t.Fatal("Squash accepted an invalid commit message")
	}
	if out.Status != StatusError {
		t.Fatalf("status = %q, want error", out.Status)
	}
	if got := gitHead(t, h.root); got != before {
		t.Fatalf("main HEAD moved by invalid-message squash: %s -> %s", before, got)
	}
	if lastEvent(h, "squashed") != nil {
		t.Fatal("invalid-message squash still emitted a squashed event")
	}
}

// TestDriverOpenReviewThreadsCountsUnresolvedAnnotations proves the
// --force-gate wrapper reports the linked PR's open diff-annotation count.
func TestDriverOpenReviewThreadsCountsUnresolvedAnnotations(t *testing.T) {
	h, d, id := driveVerified(t)
	ref := domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://github.com/o/r/pull/7", HeadSHA: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b"}
	if err := h.store.SetPullRequest(context.Background(), id, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.AddDiffAnnotation(context.Background(), domain.DiffAnnotation{
		Feature: id, File: "x.go", Anchor: "a", Excerpt: "line", Comment: "please fix",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	count, url, err := d.OpenReviewThreads(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if url != ref.URL {
		t.Fatalf("url = %q, want %q", url, ref.URL)
	}
}
