// Package model defines the normalized IAM snapshot shared by every stage of the pipeline.
package model

import (
	"encoding/json"
	"time"
)

// Snapshot is one full point-in-time copy of the IAM state of an account.
type Snapshot struct {
	RunID       string    `json:"run_id"`
	CollectedAt time.Time `json:"collected_at"`
	AccountID   string    `json:"account_id"`
	Users       []User    `json:"users"`
	Groups      []Group   `json:"groups"`
	Roles       []Role    `json:"roles"`
	Policies    []Policy  `json:"policies"`
}

// PolicyRef points at a managed policy attached to a principal.
type PolicyRef struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

// InlinePolicy is a policy embedded directly in a user, group or role.
type InlinePolicy struct {
	Name     string          `json:"name"`
	Document json.RawMessage `json:"document"`
}

// AccessKey is the metadata of a long-lived user access key (never the secret).
type AccessKey struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	CreateDate time.Time `json:"create_date"`
}

type User struct {
	Name                string         `json:"name"`
	ID                  string         `json:"id"`
	ARN                 string         `json:"arn"`
	Path                string         `json:"path"`
	CreateDate          time.Time      `json:"create_date"`
	PermissionsBoundary string         `json:"permissions_boundary,omitempty"`
	Groups              []string       `json:"groups"`
	ManagedPolicies     []PolicyRef    `json:"managed_policies"`
	InlinePolicies      []InlinePolicy `json:"inline_policies"`
	AccessKeys          []AccessKey    `json:"access_keys"`
}

type Group struct {
	Name            string         `json:"name"`
	ID              string         `json:"id"`
	ARN             string         `json:"arn"`
	Path            string         `json:"path"`
	CreateDate      time.Time      `json:"create_date"`
	ManagedPolicies []PolicyRef    `json:"managed_policies"`
	InlinePolicies  []InlinePolicy `json:"inline_policies"`
}

// RoleLastUsed is volatile (changes daily) so the diff engine ignores it.
type RoleLastUsed struct {
	Date   time.Time `json:"date"`
	Region string    `json:"region"`
}

type Role struct {
	Name                string          `json:"name"`
	ID                  string          `json:"id"`
	ARN                 string          `json:"arn"`
	Path                string          `json:"path"`
	CreateDate          time.Time       `json:"create_date"`
	TrustPolicy         json.RawMessage `json:"trust_policy"`
	PermissionsBoundary string          `json:"permissions_boundary,omitempty"`
	ManagedPolicies     []PolicyRef     `json:"managed_policies"`
	InlinePolicies      []InlinePolicy  `json:"inline_policies"`
	LastUsed            *RoleLastUsed   `json:"last_used,omitempty"`
}

// Policy is a managed policy with its default-version document.
type Policy struct {
	Name            string          `json:"name"`
	ID              string          `json:"id"`
	ARN             string          `json:"arn"`
	Path            string          `json:"path"`
	AWSManaged      bool            `json:"aws_managed"`
	IsAttachable    bool            `json:"is_attachable"`
	AttachmentCount int32           `json:"attachment_count"`
	BoundaryUsage   int32           `json:"permissions_boundary_usage_count"`
	DefaultVersion  string          `json:"default_version_id"`
	CreateDate      time.Time       `json:"create_date"`
	UpdateDate      time.Time       `json:"update_date"`
	Document        json.RawMessage `json:"document"`
}
