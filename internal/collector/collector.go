// Package collector reads the IAM state of an account into a normalized model.Snapshot.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/saluja-ji/iam-inventory/internal/model"
)

var tracer = otel.Tracer("iam-inventory/collector")

// IAMAPI is the subset of the IAM client used here; tests supply a fake.
type IAMAPI interface {
	GetAccountAuthorizationDetails(ctx context.Context, in *iam.GetAccountAuthorizationDetailsInput, optFns ...func(*iam.Options)) (*iam.GetAccountAuthorizationDetailsOutput, error)
	ListAccessKeys(ctx context.Context, in *iam.ListAccessKeysInput, optFns ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
}

// Source produces snapshots. The live AWS collector and the fixture loader both implement it.
type Source interface {
	Collect(ctx context.Context, runID string, now time.Time) (*model.Snapshot, error)
}

// Collector is the live AWS implementation of Source.
type Collector struct {
	IAM       IAMAPI
	AccountID string
	// IncludeUnattachedAWSPolicies keeps the ~1000 AWS managed policies nobody uses.
	// Off by default: it makes snapshots many MB larger with no analytic value.
	IncludeUnattachedAWSPolicies bool
}

func (c *Collector) Collect(ctx context.Context, runID string, now time.Time) (snap *model.Snapshot, err error) {
	ctx, span := tracer.Start(ctx, "collector.collect")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}()

	snap = &model.Snapshot{RunID: runID, CollectedAt: now.UTC(), AccountID: c.AccountID}
	var pages int
	var marker *string
	for {
		out, err := c.IAM.GetAccountAuthorizationDetails(ctx, &iam.GetAccountAuthorizationDetailsInput{Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("get account authorization details (page %d): %w", pages+1, err)
		}
		pages++
		for _, u := range out.UserDetailList {
			snap.Users = append(snap.Users, convertUser(u))
		}
		for _, g := range out.GroupDetailList {
			snap.Groups = append(snap.Groups, convertGroup(g))
		}
		for _, r := range out.RoleDetailList {
			snap.Roles = append(snap.Roles, convertRole(r))
		}
		for _, p := range out.Policies {
			if mp := convertPolicy(p); c.IncludeUnattachedAWSPolicies || !mp.AWSManaged || mp.AttachmentCount > 0 {
				snap.Policies = append(snap.Policies, mp)
			}
		}
		if !out.IsTruncated || out.Marker == nil {
			break
		}
		marker = out.Marker
	}

	for i := range snap.Users {
		keys, err := c.accessKeys(ctx, snap.Users[i].Name)
		if err != nil {
			return nil, err
		}
		snap.Users[i].AccessKeys = keys
	}

	span.SetAttributes(
		attribute.Int("iam.pages", pages),
		attribute.Int("iam.users", len(snap.Users)),
		attribute.Int("iam.groups", len(snap.Groups)),
		attribute.Int("iam.roles", len(snap.Roles)),
		attribute.Int("iam.policies", len(snap.Policies)),
	)
	return snap, nil
}

func (c *Collector) accessKeys(ctx context.Context, user string) ([]model.AccessKey, error) {
	var keys []model.AccessKey
	var marker *string
	for {
		out, err := c.IAM.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user), Marker: marker})
		if err != nil {
			return nil, fmt.Errorf("list access keys for %s: %w", user, err)
		}
		for _, k := range out.AccessKeyMetadata {
			keys = append(keys, model.AccessKey{
				ID: aws.ToString(k.AccessKeyId), Status: string(k.Status), CreateDate: aws.ToTime(k.CreateDate),
			})
		}
		if !out.IsTruncated || out.Marker == nil {
			return keys, nil
		}
		marker = out.Marker
	}
}

// DecodeDocument turns the URL-encoded policy document IAM returns into raw JSON.
// RFC 3986 encoding is used, so spaces are %20 and a literal '+' must be preserved.
func DecodeDocument(encoded string) json.RawMessage {
	if encoded == "" {
		return nil
	}
	dec, err := url.PathUnescape(encoded)
	if err != nil {
		dec = encoded
	}
	if json.Valid([]byte(dec)) {
		return json.RawMessage(dec)
	}
	b, _ := json.Marshal(dec) // keep unparsable content as a JSON string rather than dropping it
	return b
}

func refs(in []types.AttachedPolicy) []model.PolicyRef {
	out := make([]model.PolicyRef, 0, len(in))
	for _, p := range in {
		out = append(out, model.PolicyRef{Name: aws.ToString(p.PolicyName), ARN: aws.ToString(p.PolicyArn)})
	}
	return out
}

func inline(in []types.PolicyDetail) []model.InlinePolicy {
	out := make([]model.InlinePolicy, 0, len(in))
	for _, p := range in {
		out = append(out, model.InlinePolicy{Name: aws.ToString(p.PolicyName), Document: DecodeDocument(aws.ToString(p.PolicyDocument))})
	}
	return out
}

func boundary(b *types.AttachedPermissionsBoundary) string {
	if b == nil {
		return ""
	}
	return aws.ToString(b.PermissionsBoundaryArn)
}

func convertUser(u types.UserDetail) model.User {
	return model.User{
		Name: aws.ToString(u.UserName), ID: aws.ToString(u.UserId), ARN: aws.ToString(u.Arn),
		Path: aws.ToString(u.Path), CreateDate: aws.ToTime(u.CreateDate),
		PermissionsBoundary: boundary(u.PermissionsBoundary),
		Groups:              append([]string{}, u.GroupList...),
		ManagedPolicies:     refs(u.AttachedManagedPolicies), InlinePolicies: inline(u.UserPolicyList),
	}
}

func convertGroup(g types.GroupDetail) model.Group {
	return model.Group{
		Name: aws.ToString(g.GroupName), ID: aws.ToString(g.GroupId), ARN: aws.ToString(g.Arn),
		Path: aws.ToString(g.Path), CreateDate: aws.ToTime(g.CreateDate),
		ManagedPolicies: refs(g.AttachedManagedPolicies), InlinePolicies: inline(g.GroupPolicyList),
	}
}

func convertRole(r types.RoleDetail) model.Role {
	role := model.Role{
		Name: aws.ToString(r.RoleName), ID: aws.ToString(r.RoleId), ARN: aws.ToString(r.Arn),
		Path: aws.ToString(r.Path), CreateDate: aws.ToTime(r.CreateDate),
		TrustPolicy:         DecodeDocument(aws.ToString(r.AssumeRolePolicyDocument)),
		PermissionsBoundary: boundary(r.PermissionsBoundary),
		ManagedPolicies:     refs(r.AttachedManagedPolicies), InlinePolicies: inline(r.RolePolicyList),
	}
	if lu := r.RoleLastUsed; lu != nil && lu.LastUsedDate != nil {
		role.LastUsed = &model.RoleLastUsed{Date: *lu.LastUsedDate, Region: aws.ToString(lu.Region)}
	}
	return role
}

func convertPolicy(p types.ManagedPolicyDetail) model.Policy {
	mp := model.Policy{
		Name: aws.ToString(p.PolicyName), ID: aws.ToString(p.PolicyId), ARN: aws.ToString(p.Arn),
		Path: aws.ToString(p.Path), AWSManaged: strings.Contains(aws.ToString(p.Arn), ":iam::aws:policy/"),
		IsAttachable: p.IsAttachable, AttachmentCount: aws.ToInt32(p.AttachmentCount),
		BoundaryUsage:  aws.ToInt32(p.PermissionsBoundaryUsageCount),
		DefaultVersion: aws.ToString(p.DefaultVersionId),
		CreateDate:     aws.ToTime(p.CreateDate), UpdateDate: aws.ToTime(p.UpdateDate),
	}
	for _, v := range p.PolicyVersionList {
		if v.IsDefaultVersion {
			mp.Document = DecodeDocument(aws.ToString(v.Document))
			break
		}
	}
	return mp
}
