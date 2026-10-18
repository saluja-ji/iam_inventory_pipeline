package analyze

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/saluja-ji/iam-inventory/internal/model"
)

func snap() *model.Snapshot {
	now := time.Date(2026, 10, 18, 12, 30, 0, 0, time.UTC)
	return &model.Snapshot{
		RunID: "r1", CollectedAt: now, AccountID: "111111111111",
		Roles: []model.Role{
			{Name: "admin", ARN: "arn:aws:iam::111111111111:role/admin", TrustPolicy: json.RawMessage(`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}]}`),
				ManagedPolicies: []model.PolicyRef{{Name: "AdministratorAccess", ARN: "arn:aws:iam::aws:policy/AdministratorAccess"}}},
			{Name: "ext", ARN: "arn:aws:iam::111111111111:role/ext", TrustPolicy: json.RawMessage(`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":"sts:AssumeRole"}]}`)},
			{Name: "svc", ARN: "arn:aws:iam::111111111111:role/svc", TrustPolicy: json.RawMessage(`{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
				InlinePolicies: []model.InlinePolicy{{Name: "pr", Document: json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)}}},
		},
		Users: []model.User{{Name: "bob", ARN: "arn:aws:iam::111111111111:user/bob", AccessKeys: []model.AccessKey{
			{ID: "old", Status: "Active", CreateDate: now.AddDate(0, 0, -120)},
			{ID: "inactive", Status: "Inactive", CreateDate: now.AddDate(0, 0, -400)},
			{ID: "new", Status: "Active", CreateDate: now.AddDate(0, 0, -5)},
		}}},
		Policies: []model.Policy{
			{Name: "star", ARN: "arn:aws:iam::111111111111:policy/star", Document: json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":["*"],"Resource":"*"}]}`)},
			{Name: "AdministratorAccess", ARN: "arn:aws:iam::aws:policy/AdministratorAccess", AWSManaged: true, Document: json.RawMessage(`{"Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)},
		},
	}
}

func TestEvaluateRules(t *testing.T) {
	got := map[string]int{}
	for _, f := range Evaluate(snap(), Options{}) {
		got[f.Rule]++
	}
	want := map[string]int{RuleWildcardAdmin: 1, RuleAdminAttached: 1, RuleTrustAnyone: 1, RuleTrustExternal: 1, RulePassRoleBroad: 1, RuleOldAccessKey: 1}
	for r, n := range want {
		if got[r] != n {
			t.Errorf("rule %s: got %d want %d (all: %v)", r, got[r], n, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected rules: %v", got)
	}
}

func TestEvaluateSortedBySeverity(t *testing.T) {
	f := Evaluate(snap(), Options{})
	for i := 1; i < len(f); i++ {
		if f[i-1].Severity.Rank() < f[i].Severity.Rank() {
			t.Fatal("findings not sorted by severity")
		}
	}
}

func TestDiff(t *testing.T) {
	prev, cur := snap(), snap()
	prev.RunID, cur.RunID = "p", "c"
	cur.Roles[0].LastUsed = &model.RoleLastUsed{Date: time.Now()} // volatile, must be ignored
	cur.Roles[1].ManagedPolicies = []model.PolicyRef{{Name: "x", ARN: "arn:x"}}
	cur.Roles = append(cur.Roles, model.Role{Name: "new", ARN: "arn:new"})
	prev.Users = append(prev.Users, model.User{Name: "gone", ARN: "arn:gone"})

	d := Compare(prev, cur)
	if len(d.Roles.Added) != 1 || len(d.Roles.Modified) != 1 || d.Roles.Modified[0] != cur.Roles[1].ARN {
		t.Fatalf("roles diff wrong: %+v", d.Roles)
	}
	if len(d.Users.Removed) != 1 {
		t.Fatalf("users diff wrong: %+v", d.Users)
	}
	if Compare(nil, cur).Baseline != true {
		t.Fatal("nil prev must be baseline")
	}
}

func TestParseSeverity(t *testing.T) {
	if ParseSeverity("HIGH", Low) != High || ParseSeverity("nope", Medium) != Medium {
		t.Fatal("ParseSeverity")
	}
}
