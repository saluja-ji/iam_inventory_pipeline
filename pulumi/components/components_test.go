package components

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// mocks records every resource Pulumi would create, so the whole stack can be unit-tested offline.
type mocks struct {
	mu        sync.Mutex
	resources map[string]resource.PropertyMap // keyed by "type/name"
}

func (m *mocks) NewResource(a pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	m.mu.Lock()
	m.resources[a.TypeToken+"/"+a.Name] = a.Inputs
	m.mu.Unlock()
	out := a.Inputs.Copy()
	out["arn"] = resource.NewStringProperty("arn:aws:mock:::" + a.Name)
	out["name"] = resource.NewStringProperty(a.Name)
	return a.Name + "_id", out, nil
}

func (m *mocks) Call(a pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{"region": resource.NewStringProperty("ap-south-1")}, nil
}

func run(t *testing.T, f func(ctx *pulumi.Context) error) *mocks {
	t.Helper()
	m := &mocks{resources: map[string]resource.PropertyMap{}}
	if err := pulumi.RunErr(f, pulumi.WithMocks("iam-inventory", "test", m)); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *mocks) count(typeToken string) int {
	n := 0
	for k := range m.resources {
		if strings.HasPrefix(k, typeToken+"/") {
			n++
		}
	}
	return n
}

func TestBucketHasTLSOnlyPolicyAndExpiryRules(t *testing.T) {
	m := run(t, func(ctx *pulumi.Context) error {
		_, err := NewBucket(ctx, "data", BucketArgs{
			Name:        pulumi.String("b"),
			ExpiryRules: []ExpiryRule{{ID: "expire-raw", Prefix: "raw/", ExpireIn: 365}},
		})
		return err
	})
	for _, typ := range []string{
		"aws:s3/bucket:Bucket", "aws:s3/bucketVersioning:BucketVersioning",
		"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock", "aws:s3/bucketPolicy:BucketPolicy",
	} {
		if m.count(typ) != 1 {
			t.Errorf("want 1 %s, got %d", typ, m.count(typ))
		}
	}
	lc := m.resources["aws:s3/bucketLifecycleConfiguration:BucketLifecycleConfiguration/data-lifecycle"]
	if got := len(lc["rules"].ArrayValue()); got != 2 {
		t.Errorf("want housekeeping + 1 expiry rule, got %d", got)
	}
	var pol struct{ Statement []map[string]any }
	raw := m.resources["aws:s3/bucketPolicy:BucketPolicy/data-policy"]["policy"].StringValue()
	if err := json.Unmarshal([]byte(raw), &pol); err != nil {
		t.Fatalf("policy is not valid JSON: %v\n%s", err, raw)
	}
	if len(pol.Statement) != 1 || pol.Statement[0]["Effect"] != "Deny" {
		t.Errorf("unexpected bucket policy: %s", raw)
	}
}

func TestLambdaOtelIsOptIn(t *testing.T) {
	build := func(layer string) *mocks {
		return run(t, func(ctx *pulumi.Context) error {
			_, err := NewLambda(ctx, "lambda", LambdaArgs{
				FunctionName: "f", SourceDir: ".", AdotLayerArn: layer,
				DataBucketArn: pulumi.String("arn:aws:s3:::b"), DataBucketName: pulumi.String("b"),
				SnsTopicArn: pulumi.String("arn:aws:sns:::t"),
			})
			return err
		})
	}
	const fnType = "aws:lambda/function:Function/lambda"
	off, on := build(""), build("arn:aws:lambda:::layer:adot:1")

	if mode := off.resources[fnType]["tracingConfig"].ObjectValue()["mode"].StringValue(); mode != "PassThrough" {
		t.Errorf("tracing without ADOT = %s", mode)
	}
	if mode := on.resources[fnType]["tracingConfig"].ObjectValue()["mode"].StringValue(); mode != "Active" {
		t.Errorf("tracing with ADOT = %s", mode)
	}
	policy := func(m *mocks) string {
		return m.resources["aws:iam/rolePolicy:RolePolicy/lambda-policy"]["policy"].StringValue()
	}
	if strings.Contains(policy(off), "XRayExport") || !strings.Contains(policy(on), "XRayExport") {
		t.Error("X-Ray statements must exist only when the ADOT layer is configured")
	}
}

func TestGlueCreatesOneTablePerRowType(t *testing.T) {
	m := run(t, func(ctx *pulumi.Context) error {
		_, err := NewGlue(ctx, "glue", GlueArgs{
			Name: "iam-inventory-dev", DataBucketName: pulumi.String("d"), ResultsBucketName: pulumi.String("r"),
		})
		return err
	})
	if got := m.count("aws:glue/catalogTable:CatalogTable"); got != 8 {
		t.Errorf("want 8 Glue tables, got %d", got)
	}
	if got := m.count("aws:athena/namedQuery:NamedQuery"); got != 5 {
		t.Errorf("want 5 named queries, got %d", got)
	}
	db := m.resources["aws:glue/catalogDatabase:CatalogDatabase/glue-db"]["name"].StringValue()
	if db != "iam_inventory_dev" {
		t.Errorf("database name = %s", db)
	}
}

func TestGithubOIDCTrustIsScopedPerRole(t *testing.T) {
	m := run(t, func(ctx *pulumi.Context) error {
		_, err := NewGithubOIDC(ctx, "oidc", GithubOIDCArgs{
			Name: "n", GithubRepo: "o/r", StateBucketName: "s", CreateOIDCProvider: true,
		})
		return err
	})
	trust := func(role string) string {
		return m.resources["aws:iam/role:Role/"+role]["assumeRolePolicy"].StringValue()
	}
	if !strings.Contains(trust("oidc-plan"), "repo:o/r:pull_request") {
		t.Error("plan role must trust only pull_request")
	}
	if !strings.Contains(trust("oidc-apply"), "repo:o/r:environment:production") {
		t.Error("apply role must trust only the production environment")
	}
}
