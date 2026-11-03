package workflowrecipe

import (
	"reflect"
	"testing"
)

func TestVerdictShape(t *testing.T) {
	schema := map[string]string{"findings": "array", "blockers": "number"}
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`{"verdict": "APPROVED", "findings": [], "blockers": 0}`, nil},
		{`{"verdict": "APPROVED"}`, []string{"blockers is missing", "findings is missing"}},
		{`{"verdict": 1, "findings": {}, "blockers": "0"}`, []string{"blockers is string, not number", "findings is object, not array", "verdict is number, not string"}},
		{`Still waiting on DW-2.`, []string{"it is not a JSON object"}},
		{`[]`, []string{"it is not a JSON object"}},
	} {
		if got := VerdictShape([]byte(tc.raw), schema); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.raw, got, tc.want)
		}
	}
}
