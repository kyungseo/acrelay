package review

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kyungseo/acrelay/internal/kernel"
)

// parity fixtures — 입력·기대 오류가 FEAT-20260718-001 v3 spike의 Node/Go
// validator와 semantic 동일해야 한다.
func TestValidateResultParityFixtures(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{"approve-empty", `{"verdict":"approve","findings":[]}`, nil},
		{"bad-verdict", `{"verdict":"maybe","findings":["x"]}`, []string{"verdict-enum:maybe"}},
		{"empty-finding-string", `{"verdict":"approve","findings":[""]}`, []string{"empty-finding:0"}},
		{"missing-field", `{"verdict":"approve"}`, []string{"missing:findings"}},
		{"unknown-property", `{"verdict":"approve","findings":[],"extra":1}`, []string{"unknown-property:extra"}},
		{"changes-requested-empty", `{"verdict":"changes-requested","findings":[]}`, []string{"changes-requested-needs-finding"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(c.input), &m); err != nil {
				t.Fatal(err)
			}
			got := ValidateResult(m)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestClassifyOutcome(t *testing.T) {
	if o := ClassifyOutcome(kernel.ExecSucceeded, nil); o != OutcomeResultValid {
		t.Fatalf("got %s", o)
	}
	if o := ClassifyOutcome(kernel.ExecSucceeded, []string{"verdict-enum:maybe"}); o != OutcomeNeedsInput {
		t.Fatalf("needs-input is a dispatch outcome, got %s", o)
	}
	if o := ClassifyOutcome(kernel.ExecFailed, nil); o != OutcomeFailed {
		t.Fatalf("got %s", o)
	}
	if o := ClassifyOutcome(kernel.ExecUnknown, nil); o != OutcomeFailed {
		t.Fatalf("UNKNOWN must never classify as valid, got %s", o)
	}
}

func TestClosureCheckFailClosed(t *testing.T) {
	findings := []Finding{
		{ID: "F1", Blocking: true},
		{ID: "F2", Blocking: false}, // non-blocking never blocks closure
	}
	if err := ClosureCheck(findings); err == nil {
		t.Fatal("undispositioned blocking finding must block closure")
	}
	findings[0].Disposition = DispositionNeedsUser
	if err := ClosureCheck(findings); err == nil {
		t.Fatal("needs-user without arbiter decision must block closure")
	}
	findings[0].Decided = true
	if err := ClosureCheck(findings); err != nil {
		t.Fatal(err)
	}
	// severity와 blocking 분리: high severity라도 non-blocking이면 closure 무관
	findings = append(findings, Finding{ID: "F3", Severity: "P1", Blocking: false})
	if err := ClosureCheck(findings); err != nil {
		t.Fatal("non-blocking high-severity finding must not block closure")
	}
}
