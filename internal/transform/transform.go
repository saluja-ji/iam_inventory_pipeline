// Package transform flattens a nested snapshot into relational rows and encodes them as Parquet.
// Column names here are mirrored by terraform/modules/glue; keep them in sync (see TestSchemas).
package transform

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/saluja-ji/iam-inventory/internal/analyze"
	"github.com/saluja-ji/iam-inventory/internal/model"
	"github.com/saluja-ji/iam-inventory/internal/policydoc"
)

const ts = "timestamp(millisecond)"

type RoleRow struct {
	RunID             string    `parquet:"run_id"`
	RoleName          string    `parquet:"role_name"`
	RoleID            string    `parquet:"role_id"`
	ARN               string    `parquet:"arn"`
	Path              string    `parquet:"path"`
	CreateDate        time.Time `parquet:"create_date,timestamp(millisecond)"`
	BoundaryARN       string    `parquet:"permissions_boundary_arn,optional"`
	LastUsedDate      time.Time `parquet:"last_used_date,optional,timestamp(millisecond)"`
	LastUsedRegion    string    `parquet:"last_used_region,optional"`
	ManagedPolicyCnt  int32     `parquet:"managed_policy_count"`
	InlinePolicyCount int32     `parquet:"inline_policy_count"`
	TrustPolicy       string    `parquet:"trust_policy"`
}

type UserRow struct {
	RunID             string    `parquet:"run_id"`
	UserName          string    `parquet:"user_name"`
	UserID            string    `parquet:"user_id"`
	ARN               string    `parquet:"arn"`
	Path              string    `parquet:"path"`
	CreateDate        time.Time `parquet:"create_date,timestamp(millisecond)"`
	BoundaryARN       string    `parquet:"permissions_boundary_arn,optional"`
	GroupCount        int32     `parquet:"group_count"`
	ManagedPolicyCnt  int32     `parquet:"managed_policy_count"`
	InlinePolicyCount int32     `parquet:"inline_policy_count"`
	AccessKeyCount    int32     `parquet:"access_key_count"`
}

type GroupRow struct {
	RunID             string    `parquet:"run_id"`
	GroupName         string    `parquet:"group_name"`
	GroupID           string    `parquet:"group_id"`
	ARN               string    `parquet:"arn"`
	Path              string    `parquet:"path"`
	CreateDate        time.Time `parquet:"create_date,timestamp(millisecond)"`
	ManagedPolicyCnt  int32     `parquet:"managed_policy_count"`
	InlinePolicyCount int32     `parquet:"inline_policy_count"`
}

type PolicyRow struct {
	RunID           string    `parquet:"run_id"`
	PolicyName      string    `parquet:"policy_name"`
	PolicyID        string    `parquet:"policy_id"`
	ARN             string    `parquet:"arn"`
	Path            string    `parquet:"path"`
	IsAWSManaged    bool      `parquet:"is_aws_managed"`
	IsAttachable    bool      `parquet:"is_attachable"`
	AttachmentCount int32     `parquet:"attachment_count"`
	BoundaryUsage   int32     `parquet:"permissions_boundary_usage_count"`
	DefaultVersion  string    `parquet:"default_version_id"`
	CreateDate      time.Time `parquet:"create_date,timestamp(millisecond)"`
	UpdateDate      time.Time `parquet:"update_date,timestamp(millisecond)"`
	Document        string    `parquet:"document"`
}

type RoleAttachmentRow struct {
	RunID      string `parquet:"run_id"`
	RoleName   string `parquet:"role_name"`
	RoleARN    string `parquet:"role_arn"`
	PolicyType string `parquet:"policy_type"` // managed | inline
	PolicyName string `parquet:"policy_name"`
	PolicyARN  string `parquet:"policy_arn,optional"`
}

type TrustRow struct {
	RunID         string `parquet:"run_id"`
	RoleName      string `parquet:"role_name"`
	RoleARN       string `parquet:"role_arn"`
	Effect        string `parquet:"effect"`
	PrincipalType string `parquet:"principal_type"`
	Principal     string `parquet:"principal"`
	Action        string `parquet:"action"`
	HasCondition  bool   `parquet:"has_condition"`
}

type AccessKeyRow struct {
	RunID       string    `parquet:"run_id"`
	UserName    string    `parquet:"user_name"`
	AccessKeyID string    `parquet:"access_key_id"`
	Status      string    `parquet:"status"`
	CreateDate  time.Time `parquet:"create_date,timestamp(millisecond)"`
	AgeDays     int32     `parquet:"age_days"`
}

type FindingRow struct {
	RunID        string `parquet:"run_id"`
	Rule         string `parquet:"rule"`
	Severity     string `parquet:"severity"`
	ResourceType string `parquet:"resource_type"`
	ResourceName string `parquet:"resource_name"`
	ARN          string `parquet:"arn"`
	Detail       string `parquet:"detail"`
}

// Table names, shared with Terraform.
const (
	TRoles       = "roles"
	TUsers       = "users"
	TGroups      = "groups"
	TPolicies    = "policies"
	TAttachments = "role_policy_attachments"
	TTrust       = "trust_relationships"
	TAccessKeys  = "access_keys"
	TFindings    = "findings"
)

// Build returns table name -> Parquet bytes for every curated table.
func Build(s *model.Snapshot, findings []analyze.Finding) (map[string][]byte, error) {
	var (
		roles  []RoleRow
		users  []UserRow
		groups []GroupRow
		pols   []PolicyRow
		atts   []RoleAttachmentRow
		trusts []TrustRow
		keys   []AccessKeyRow
		finds  []FindingRow
	)
	for _, r := range s.Roles {
		row := RoleRow{RunID: s.RunID, RoleName: r.Name, RoleID: r.ID, ARN: r.ARN, Path: r.Path, CreateDate: r.CreateDate,
			BoundaryARN: r.PermissionsBoundary, ManagedPolicyCnt: int32(len(r.ManagedPolicies)),
			InlinePolicyCount: int32(len(r.InlinePolicies)), TrustPolicy: string(r.TrustPolicy)}
		if r.LastUsed != nil {
			row.LastUsedDate, row.LastUsedRegion = r.LastUsed.Date, r.LastUsed.Region
		}
		roles = append(roles, row)
		for _, p := range r.ManagedPolicies {
			atts = append(atts, RoleAttachmentRow{s.RunID, r.Name, r.ARN, "managed", p.Name, p.ARN})
		}
		for _, p := range r.InlinePolicies {
			atts = append(atts, RoleAttachmentRow{s.RunID, r.Name, r.ARN, "inline", p.Name, ""})
		}
		t, err := trustRows(s.RunID, r)
		if err != nil {
			return nil, err
		}
		trusts = append(trusts, t...)
	}
	for _, u := range s.Users {
		users = append(users, UserRow{s.RunID, u.Name, u.ID, u.ARN, u.Path, u.CreateDate, u.PermissionsBoundary,
			int32(len(u.Groups)), int32(len(u.ManagedPolicies)), int32(len(u.InlinePolicies)), int32(len(u.AccessKeys))})
		for _, k := range u.AccessKeys {
			keys = append(keys, AccessKeyRow{s.RunID, u.Name, k.ID, k.Status, k.CreateDate, int32(analyze.KeyAge(s.CollectedAt, k.CreateDate))})
		}
	}
	for _, g := range s.Groups {
		groups = append(groups, GroupRow{s.RunID, g.Name, g.ID, g.ARN, g.Path, g.CreateDate,
			int32(len(g.ManagedPolicies)), int32(len(g.InlinePolicies))})
	}
	for _, p := range s.Policies {
		pols = append(pols, PolicyRow{s.RunID, p.Name, p.ID, p.ARN, p.Path, p.AWSManaged, p.IsAttachable,
			p.AttachmentCount, p.BoundaryUsage, p.DefaultVersion, p.CreateDate, p.UpdateDate, string(p.Document)})
	}
	for _, f := range findings {
		finds = append(finds, FindingRow{s.RunID, f.Rule, string(f.Severity), f.ResourceType, f.ResourceName, f.ARN, f.Detail})
	}

	out := map[string][]byte{}
	var err error
	if out[TRoles], err = encode(roles); err != nil {
		return nil, err
	}
	if out[TUsers], err = encode(users); err != nil {
		return nil, err
	}
	if out[TGroups], err = encode(groups); err != nil {
		return nil, err
	}
	if out[TPolicies], err = encode(pols); err != nil {
		return nil, err
	}
	if out[TAttachments], err = encode(atts); err != nil {
		return nil, err
	}
	if out[TTrust], err = encode(trusts); err != nil {
		return nil, err
	}
	if out[TAccessKeys], err = encode(keys); err != nil {
		return nil, err
	}
	if out[TFindings], err = encode(finds); err != nil {
		return nil, err
	}
	return out, nil
}

func trustRows(runID string, r model.Role) ([]TrustRow, error) {
	if len(r.TrustPolicy) == 0 {
		return nil, nil
	}
	doc, err := policydoc.Parse(r.TrustPolicy)
	if err != nil {
		return nil, fmt.Errorf("trust policy of %s: %w", r.Name, err)
	}
	var rows []TrustRow
	for _, st := range doc.Statements {
		for ptype, vals := range st.Principal {
			for _, v := range vals {
				rows = append(rows, TrustRow{runID, r.Name, r.ARN, st.Effect, ptype, v,
					strings.Join(st.Actions, ","), st.HasCondition})
			}
		}
	}
	return rows, nil
}

func encode[T any](rows []T) ([]byte, error) {
	var buf bytes.Buffer
	if err := parquet.Write(&buf, rows); err != nil {
		return nil, fmt.Errorf("encode parquet: %w", err)
	}
	return buf.Bytes(), nil
}
