package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTaskListReadsEveryBackendsTaskTool(t *testing.T) {
	args := func(s string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	want := []Task{
		{Text: "Read the code", Status: TaskDone},
		{Text: "Fixing the bug", Status: TaskInProgress},
		{Text: "Run the tests", Status: TaskPending},
	}
	cases := map[string]struct {
		tool string
		args string
	}{
		"claude TodoWrite": {"TodoWrite", `{"todos":[
			{"content":"Read the code","status":"completed","activeForm":"Reading the code"},
			{"content":"Fix the bug","status":"in_progress","activeForm":"Fixing the bug"},
			{"content":"Run the tests","status":"pending","activeForm":"Running the tests"}]}`},
		"opencode todowrite": {"todowrite", `{"todos":[
			{"content":"Read the code","status":"completed","id":"1"},
			{"content":"Fixing the bug","status":"in_progress","id":"2"},
			{"content":"Run the tests","status":"pending","id":"3"}]}`},
		"codex update_plan": {"update_plan", `{"plan":[
			{"step":"Read the code","status":"completed"},
			{"step":"Fixing the bug","status":"in_progress"},
			{"step":"Run the tests","status":"pending"}]}`},
		"copilot update_todo": {"update_todo", `{"todos":"- [x] Read the code\n- [~] Fixing the bug\n- [ ] Run the tests\nnot a task"}`},
	}
	for name, c := range cases {
		got, ok := TaskList(c.tool, args(c.args))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: TaskList = %#v, %v", name, got, ok)
		}
	}
	if _, ok := TaskList("Bash", args(`{"command":"ls"}`)); ok {
		t.Error("an ordinary tool read as a task list")
	}
	if got, ok := TaskList("TodoWrite", args(`{"todos":[]}`)); !ok || len(got) != 0 {
		t.Errorf("a cleared list = %#v, %v; want an empty list that counts", got, ok)
	}
}

func TestCodexTodoListIsATaskList(t *testing.T) {
	s := &codexSession{}
	for _, typ := range []string{"item.started", "item.updated", "item.completed"} {
		evs, _, err := s.mapLine([]byte(`{"type":"` + typ + `","item":{"id":"t1","type":"todo_list","items":[{"text":"a","completed":true},{"text":"b","completed":false}]}}`))
		want := []Task{{Text: "a", Status: TaskDone}, {Text: "b", Status: TaskPending}}
		if err != nil || len(evs) != 1 || evs[0].Kind != EventTasks || !reflect.DeepEqual(evs[0].Tasks, want) {
			t.Fatalf("%s: %#v, %v", typ, evs, err)
		}
	}
	// an update to any other item is not something gummi reads yet
	evs, _, err := s.mapLine([]byte(`{"type":"item.updated","item":{"id":"c1","type":"command_execution","command":"ls"}}`))
	if err != nil || len(evs) != 0 {
		t.Fatalf("other update = %#v, %v", evs, err)
	}
}

func TestHeadlessTasksFrame(t *testing.T) {
	ev, ok := decodeHeadless([]byte(`{"type":"tasks","tasks":[{"text":"a","status":"done"},{"text":"b","status":"in_progress"}]}`))
	want := []Task{{Text: "a", Status: TaskDone}, {Text: "b", Status: TaskInProgress}}
	if !ok || ev.Kind != EventTasks || !reflect.DeepEqual(ev.Tasks, want) {
		t.Fatalf("tasks frame = %#v, %v", ev, ok)
	}
}
