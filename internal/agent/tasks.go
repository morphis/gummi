package agent

import "strings"

// TaskStatus is where one item of an agent's checklist stands.
type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "completed"
)

// Task is one item of the checklist an agent keeps for itself while it
// works. Every backend's task tool replaces the whole list on each call,
// so a list is always read whole, never merged.
type Task struct {
	Text   string     `json:"text"`
	Status TaskStatus `json:"status"`
}

// TaskList reads a backend's own task-tool call as the checklist it
// sets: Claude Code's TodoWrite and opencode's todowrite ({todos:
// [{content, status, activeForm}]}), Codex's update_plan ({plan: [{step,
// status}]}) and Copilot's update_todo, whose todos is a markdown
// checklist. ok is false for any other tool, or one whose arguments hold
// no list at all — an empty list that parsed is a real "cleared".
func TaskList(tool string, args map[string]any) ([]Task, bool) {
	switch strings.ToLower(tool) {
	case "todowrite", "update_todo", "todo_write":
		if md, ok := args["todos"].(string); ok {
			return markdownTasks(md), true
		}
		return taskItems(args["todos"], "content", "activeForm")
	case "update_plan":
		return taskItems(args["plan"], "step", "")
	}
	return nil, false
}

// taskItems maps a JSON array of task objects. While an item is in
// progress Claude Code also carries its present-tense form (activeForm,
// "Running the tests"), which reads better as the line that is moving.
func taskItems(v any, textKey, activeKey string) ([]Task, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]Task, 0, len(items))
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		t := Task{Status: taskStatus(m["status"])}
		t.Text, _ = m[textKey].(string)
		if a, _ := m[activeKey].(string); a != "" && t.Status == TaskInProgress {
			t.Text = a
		}
		if t.Text = strings.TrimSpace(t.Text); t.Text != "" {
			out = append(out, t)
		}
	}
	return out, true
}

// taskStatus folds each backend's status words onto three.
func taskStatus(v any) TaskStatus {
	s, _ := v.(string)
	switch strings.ToLower(s) {
	case "in_progress", "in-progress", "active", "running":
		return TaskInProgress
	case "completed", "complete", "done":
		return TaskDone
	}
	return TaskPending
}

// markdownTasks reads a "- [ ] / - [x]" checklist; any other line is not
// a task.
func markdownTasks(md string) []Task {
	out := []Task{}
	for _, line := range strings.Split(md, "\n") {
		l := strings.TrimSpace(line)
		l = strings.TrimLeft(l, "-*+ ")
		var st TaskStatus
		switch {
		case strings.HasPrefix(l, "[ ]"):
			st = TaskPending
		case strings.HasPrefix(l, "[x]"), strings.HasPrefix(l, "[X]"):
			st = TaskDone
		case strings.HasPrefix(l, "[~]"), strings.HasPrefix(l, "[>]"):
			st = TaskInProgress
		default:
			continue
		}
		if text := strings.TrimSpace(l[3:]); text != "" {
			out = append(out, Task{Text: text, Status: st})
		}
	}
	return out
}

// tasksEvent is the EventTasks a task-tool call also yields, or nil.
func tasksEvent(tool string, args map[string]any) []Event {
	if ts, ok := TaskList(tool, args); ok {
		return []Event{{Kind: EventTasks, Tool: tool, Tasks: ts}}
	}
	return nil
}

// FormatTasks writes a list as the markdown checklist ParseTasks reads
// back: the form a list is kept in where only text can go.
func FormatTasks(ts []Task) string {
	var b strings.Builder
	for _, t := range ts {
		mark := "[ ]"
		switch t.Status {
		case TaskDone:
			mark = "[x]"
		case TaskInProgress:
			mark = "[~]"
		}
		b.WriteString("- " + mark + " " + strings.Join(strings.Fields(t.Text), " ") + "\n")
	}
	return b.String()
}

// ParseTasks reads a FormatTasks checklist.
func ParseTasks(md string) []Task { return markdownTasks(md) }
