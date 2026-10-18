package collector

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
)

type fakeIAM struct{ calls int }

func (f *fakeIAM) GetAccountAuthorizationDetails(_ context.Context, in *iam.GetAccountAuthorizationDetailsInput, _ ...func(*iam.Options)) (*iam.GetAccountAuthorizationDetailsOutput, error) {
	f.calls++
	trust := url.PathEscape(`{"Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)
	if in.Marker == nil {
		return &iam.GetAccountAuthorizationDetailsOutput{
			IsTruncated: true, Marker: aws.String("m1"),
			RoleDetailList: []types.RoleDetail{{RoleName: aws.String("r1"), Arn: aws.String("arn:aws:iam::1:role/r1"), AssumeRolePolicyDocument: aws.String(trust)}},
			UserDetailList: []types.UserDetail{{UserName: aws.String("alice")}},
		}, nil
	}
	return &iam.GetAccountAuthorizationDetailsOutput{
		RoleDetailList: []types.RoleDetail{{RoleName: aws.String("r2")}},
		Policies: []types.ManagedPolicyDetail{
			{PolicyName: aws.String("Unused"), Arn: aws.String("arn:aws:iam::aws:policy/Unused")},
			{PolicyName: aws.String("Mine"), Arn: aws.String("arn:aws:iam::1:policy/Mine"), PolicyVersionList: []types.PolicyVersion{
				{IsDefaultVersion: false, Document: aws.String("old")},
				{IsDefaultVersion: true, Document: aws.String(url.PathEscape(`{"Statement":[]}`))},
			}},
		},
	}, nil
}

func (f *fakeIAM) ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	return &iam.ListAccessKeysOutput{AccessKeyMetadata: []types.AccessKeyMetadata{
		{AccessKeyId: aws.String("AKIAX"), Status: types.StatusTypeActive, CreateDate: aws.Time(time.Unix(0, 0))},
	}}, nil
}

func TestCollectPaginatesAndDecodes(t *testing.T) {
	f := &fakeIAM{}
	c := &Collector{IAM: f, AccountID: "1"}
	snap, err := c.Collect(context.Background(), "run", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 || len(snap.Roles) != 2 {
		t.Fatalf("pagination: calls=%d roles=%d", f.calls, len(snap.Roles))
	}
	if string(snap.Roles[0].TrustPolicy) == "" || snap.Roles[0].TrustPolicy[0] != '{' {
		t.Fatalf("trust policy not decoded: %s", snap.Roles[0].TrustPolicy)
	}
	if len(snap.Policies) != 1 || snap.Policies[0].Name != "Mine" || string(snap.Policies[0].Document) != `{"Statement":[]}` {
		t.Fatalf("policy filter/default version wrong: %+v", snap.Policies)
	}
	if len(snap.Users[0].AccessKeys) != 1 {
		t.Fatal("access keys not collected")
	}
}

func TestIncludeUnattachedAWSPolicies(t *testing.T) {
	c := &Collector{IAM: &fakeIAM{}, IncludeUnattachedAWSPolicies: true}
	snap, _ := c.Collect(context.Background(), "r", time.Now())
	if len(snap.Policies) != 2 {
		t.Fatalf("want 2 policies, got %d", len(snap.Policies))
	}
}

func TestDecodeDocumentKeepsPlus(t *testing.T) {
	got := string(DecodeDocument(url.PathEscape(`{"a":"b+c d"}`)))
	if got != `{"a":"b+c d"}` {
		t.Fatalf("got %s", got)
	}
}
