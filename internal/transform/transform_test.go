package transform

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/saluja-ji/iam-inventory/internal/analyze"
	"github.com/saluja-ji/iam-inventory/internal/model"
)

func sample() *model.Snapshot {
	now := time.Date(2026, 10, 18, 12, 30, 0, 0, time.UTC)
	return &model.Snapshot{
		RunID: "r1", CollectedAt: now, AccountID: "1",
		Roles: []model.Role{
			{Name: "a", ARN: "arn:a", CreateDate: now, TrustPolicy: json.RawMessage(`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["arn:aws:iam::2:root","arn:aws:iam::3:root"]},"Action":"sts:AssumeRole"}]}`),
				ManagedPolicies: []model.PolicyRef{{Name: "P", ARN: "arn:p"}}, InlinePolicies: []model.InlinePolicy{{Name: "i"}},
				LastUsed: &model.RoleLastUsed{Date: now, Region: "us-east-1"}},
			{Name: "b", ARN: "arn:b", CreateDate: now}, // never used, no trust policy
		},
		Users: []model.User{{Name: "u", AccessKeys: []model.AccessKey{{ID: "k", Status: "Active", CreateDate: now.AddDate(0, 0, -10)}}}},
	}
}

func TestBuildRoundTrip(t *testing.T) {
	tables, err := Build(sample(), []analyze.Finding{{Rule: "R", Severity: analyze.High}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{TRoles, TUsers, TGroups, TPolicies, TAttachments, TTrust, TAccessKeys, TFindings} {
		if len(tables[name]) == 0 {
			t.Fatalf("table %s is empty/missing", name)
		}
	}

	roles, err := parquet.Read[RoleRow](bytes.NewReader(tables[TRoles]), int64(len(tables[TRoles])))
	if err != nil || len(roles) != 2 {
		t.Fatalf("roles read: %v %d", err, len(roles))
	}
	if roles[0].LastUsedRegion != "us-east-1" || roles[0].LastUsedDate.IsZero() || roles[1].LastUsedRegion != "" {
		t.Fatalf("optional columns wrong: %+v / %+v", roles[0], roles[1])
	}
	trust, _ := parquet.Read[TrustRow](bytes.NewReader(tables[TTrust]), int64(len(tables[TTrust])))
	if len(trust) != 2 || trust[0].PrincipalType != "AWS" {
		t.Fatalf("trust rows: %+v", trust)
	}
	keys, _ := parquet.Read[AccessKeyRow](bytes.NewReader(tables[TAccessKeys]), int64(len(tables[TAccessKeys])))
	if len(keys) != 1 || keys[0].AgeDays != 10 {
		t.Fatalf("key rows: %+v", keys)
	}
}

func TestEmptyTablesAreValidParquet(t *testing.T) {
	tables, err := Build(&model.Snapshot{RunID: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parquet.Read[PolicyRow](bytes.NewReader(tables[TPolicies]), int64(len(tables[TPolicies])))
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty table: %v %d", err, len(rows))
	}
}
