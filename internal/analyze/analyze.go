// Package analyze applies risk rules to a snapshot and diffs snapshots over time.
package analyze

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/saluja-ji/iam-inventory/internal/model"
	"github.com/saluja-ji/iam-inventory/internal/policydoc"
)

type Severity string

const (
	Low      Severity = "low"
	Medium   Severity = "medium"
	High     Severity = "high"
	Critical Severity = "critical"
)

func (s Severity) Rank() int {
	switch s {
	case Critical:
		return 4
	case High:
		return 3
	case Medium:
		return 2
	case Low:
		return 1
	}
	return 0
}

// ParseSeverity returns def for unknown input.
func ParseSeverity(s string, def Severity) Severity {
	v := Severity(strings.ToLower(strings.TrimSpace(s)))
	if v.Rank() == 0 {
		return def
	}
	return v
}

const (
	RuleWildcardAdmin   = "WILDCARD_ADMIN"
	RuleAdminAttached   = "ADMIN_POLICY_ATTACHED"
	RulePassRoleBroad   = "PASSROLE_BROAD_RESOURCE"
	RuleTrustAnyone     = "TRUST_ANY_PRINCIPAL"
	RuleTrustExternal   = "TRUST_EXTERNAL_ACCOUNT"
	RuleOldAccessKey    = "OLD_ACCESS_KEY"
	adminPolicyARNSufix = ":iam::aws:policy/AdministratorAccess"
)

type Finding struct {
	Rule         string   `json:"rule"`
	Severity     Severity `json:"severity"`
	ResourceType string   `json:"resource_type"`
	ResourceName string   `json:"resource_name"`
	ARN          string   `json:"arn"`
	Detail       string   `json:"detail"`
}

type Options struct {
	MaxKeyAgeDays int
}

// Evaluate runs every rule. Output is sorted for stable diffs and tests.
func Evaluate(s *model.Snapshot, opts Options) []Finding {
	if opts.MaxKeyAgeDays <= 0 {
		opts.MaxKeyAgeDays = 90
	}
	var out []Finding

	// Customer-managed policy documents are checked once, not per attachment.
	for _, p := range s.Policies {
		if !p.AWSManaged {
			out = append(out, checkPolicy("policy", p.Name, p.ARN, p.Document)...)
		}
	}
	for _, r := range s.Roles {
		for _, ip := range r.InlinePolicies {
			out = append(out, checkPolicy("role", r.Name, r.ARN, ip.Document, "inline policy "+ip.Name)...)
		}
		out = append(out, adminAttached("role", r.Name, r.ARN, r.ManagedPolicies)...)
		out = append(out, checkTrust(r, s.AccountID)...)
	}
	for _, u := range s.Users {
		for _, ip := range u.InlinePolicies {
			out = append(out, checkPolicy("user", u.Name, u.ARN, ip.Document, "inline policy "+ip.Name)...)
		}
		out = append(out, adminAttached("user", u.Name, u.ARN, u.ManagedPolicies)...)
		for _, k := range u.AccessKeys {
			age := int(s.CollectedAt.Sub(k.CreateDate).Hours() / 24)
			if k.Status == "Active" && age > opts.MaxKeyAgeDays {
				out = append(out, Finding{RuleOldAccessKey, Medium, "user", u.Name, u.ARN,
					fmt.Sprintf("active access key %s is %d days old (limit %d)", k.ID, age, opts.MaxKeyAgeDays)})
			}
		}
	}
	for _, g := range s.Groups {
		for _, ip := range g.InlinePolicies {
			out = append(out, checkPolicy("group", g.Name, g.ARN, ip.Document, "inline policy "+ip.Name)...)
		}
		out = append(out, adminAttached("group", g.Name, g.ARN, g.ManagedPolicies)...)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.ARN < b.ARN
	})
	return out
}

func adminAttached(typ, name, arn string, refs []model.PolicyRef) []Finding {
	var out []Finding
	for _, r := range refs {
		if strings.HasSuffix(r.ARN, adminPolicyARNSufix) {
			out = append(out, Finding{RuleAdminAttached, High, typ, name, arn, "AdministratorAccess is attached"})
		}
	}
	return out
}

func checkPolicy(typ, name, arn string, raw []byte, where ...string) []Finding {
	if len(raw) == 0 {
		return nil
	}
	doc, err := policydoc.Parse(raw)
	if err != nil {
		return nil
	}
	suffix := ""
	if len(where) > 0 {
		suffix = " (" + where[0] + ")"
	}
	var out []Finding
	for _, st := range doc.Statements {
		if !st.Allow() {
			continue
		}
		switch {
		case st.MatchesAction("*") && st.AllResources():
			out = append(out, Finding{RuleWildcardAdmin, Critical, typ, name, arn, "Allow Action:* on Resource:*" + suffix})
		case (st.MatchesAction("iam:PassRole") || st.MatchesAction("iam:*")) && st.AllResources():
			out = append(out, Finding{RulePassRoleBroad, High, typ, name, arn, "iam:PassRole allowed on Resource:*" + suffix})
		}
	}
	return out
}

func checkTrust(r model.Role, accountID string) []Finding {
	if len(r.TrustPolicy) == 0 {
		return nil
	}
	doc, err := policydoc.Parse(r.TrustPolicy)
	if err != nil {
		return nil
	}
	var out []Finding
	for _, st := range doc.Statements {
		if !st.Allow() {
			continue
		}
		for _, p := range st.Principal["AWS"] {
			switch {
			case p == "*":
				sev := Critical
				if st.HasCondition {
					sev = Medium
				}
				out = append(out, Finding{RuleTrustAnyone, sev, "role", r.Name, r.ARN,
					fmt.Sprintf("trust policy allows any AWS principal (condition present: %t)", st.HasCondition)})
			default:
				if acct := principalAccount(p); acct != "" && accountID != "" && acct != accountID {
					sev := High
					if st.HasCondition {
						sev = Medium
					}
					out = append(out, Finding{RuleTrustExternal, sev, "role", r.Name, r.ARN,
						fmt.Sprintf("trusts external account %s (condition present: %t)", acct, st.HasCondition)})
				}
			}
		}
	}
	return out
}

// principalAccount extracts the account ID from an ARN or bare account ID.
func principalAccount(p string) string {
	if !strings.HasPrefix(p, "arn:") {
		if len(p) == 12 && strings.Trim(p, "0123456789") == "" {
			return p
		}
		return ""
	}
	parts := strings.Split(p, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

// KeyAge is exported for the transform layer.
func KeyAge(collected, created time.Time) int { return int(collected.Sub(created).Hours() / 24) }
