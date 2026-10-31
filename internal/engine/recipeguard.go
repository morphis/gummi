package engine

import (
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// A recipe is text an agent or a planner wrote, and recipe code reads it
// before anyone has judged it. A panic in that code fails the call or the
// step it was reading for, never the process: a planner's block that crashed
// gummi would be read again on the restart.

// The recipe checks the engine runs. They are variables so a test can make
// recipe code panic.
var (
	checkRecipe      = workflowrecipe.Check
	checkReplacement = workflowrecipe.CheckReplacement
)

// recoverRecipe turns a panic into an error in *err, logged with its stack.
// Call it deferred, with the function's named error result.
func recoverRecipe(what string, err *error) {
	if p := recover(); p != nil {
		slog.Error("recipe code panicked", "while", what, "panic", p, "stack", string(debug.Stack()))
		*err = fmt.Errorf("gummi failed %s (%v): the recipe is refused", what, p)
	}
}

// safeCheck is workflowrecipe.Check; a panic reads as a problem.
func safeCheck(r domain.Recipe) (problems, warnings []string) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("recipe check panicked", "panic", p, "stack", string(debug.Stack()))
			problems = []string{fmt.Sprintf("the recipe could not be checked (%v)", p)}
		}
	}()
	problems, warnings = checkRecipe(r)
	return append(problems, agentProblems(r.Steps)...), warnings
}

// agentProblems refuses a step that names an agent gummi has no adapter
// for, or a model that agent refuses: the same refusal a session's own
// picker gives (CheckSessionModel). It reads every step a run may start —
// parallel and repeat bodies, a watch handler's steps and recipe, and the
// recipe a dispatch froze. A step naming only a model is modelProblems'.
func agentProblems(es []domain.Entry) []string {
	var out []string
	workflowrecipe.Walk(es, func(s domain.Step, _ []domain.Entry) {
		// a misspelt name is Check's to report
		if s.Agent != "" && workflowrecipe.AgentName(s.Agent) {
			if err := CheckSessionModel(s.Agent, s.Model); err != nil {
				out = append(out, fmt.Sprintf("step %s: %v", s.ID, err))
			}
		}
		if d := s.Dispatch; d != nil && strings.TrimSpace(d.RecipeYAML) != "" {
			// a frozen recipe that does not parse is Check's to report
			if r, err := workflowrecipe.Parse([]byte(d.RecipeYAML)); err == nil {
				for _, p := range agentProblems(r.Steps) {
					out = append(out, fmt.Sprintf("dispatch %s: %s", s.ID, p))
				}
			}
		}
	})
	return out
}

// modelProblems refuses a step that names a model and no agent when the
// backend profile gives it cannot run that model. A dispatched recipe's
// steps run on its own session's profile, and are checked when they start.
func (e *Engine) modelProblems(profile string, es []domain.Entry) []string {
	var out []string
	workflowrecipe.Walk(es, func(s domain.Step, _ []domain.Entry) {
		if s.Agent == "" {
			if err := e.stepModelProblem(profile, s); err != nil {
				out = append(out, err.Error())
			}
		}
	})
	return out
}

// safeCheckReplacement is workflowrecipe.CheckReplacement, likewise.
func safeCheckReplacement(r domain.Recipe, done int, steps []domain.Entry) (out domain.Recipe, problems, warnings []string) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("revised plan check panicked", "panic", p, "stack", string(debug.Stack()))
			out, problems = r, []string{fmt.Sprintf("the revised plan could not be checked (%v)", p)}
		}
	}()
	out, problems, warnings = checkReplacement(r, done, steps)
	return out, append(problems, agentProblems(steps)...), warnings
}

// goWorkflow is goBrief for the work a run starts off its tick (a step's
// start and end, an until, the run's end): a panic in it, recipe code
// included, is logged and ends that work, never the process.
func (e *Engine) goWorkflow(fn func()) bool {
	return e.goBrief(func() {
		defer func() {
			if p := recover(); p != nil {
				slog.Error("workflow work panicked", "panic", p, "stack", string(debug.Stack()))
			}
		}()
		fn()
	})
}
