package components

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/athena"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/glue"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/saluja-ji/iam-inventory/internal/transform"
)

type GlueArgs struct {
	Name              string
	DataBucketName    pulumi.StringInput
	ResultsBucketName pulumi.StringInput
	ProjectionStart   string // first dt partition Athena considers (yyyy-MM-dd)
	ScanLimitBytes    int    // per-query scan cap (AWS minimum is 10 MB)
}

// Glue is the catalog database, one partition-projected table per Parquet row type, and an Athena workgroup.
type Glue struct {
	pulumi.ResourceState

	DatabaseName pulumi.StringOutput
	Workgroup    pulumi.StringOutput
}

func NewGlue(ctx *pulumi.Context, name string, args GlueArgs, opts ...pulumi.ResourceOption) (*Glue, error) {
	if args.ProjectionStart == "" {
		args.ProjectionStart = "2026-01-01"
	}
	if args.ScanLimitBytes == 0 {
		args.ScanLimitBytes = 1 << 30
	}
	c := &Glue{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:Glue", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)

	db, err := glue.NewCatalogDatabase(ctx, name+"-db", &glue.CatalogDatabaseArgs{
		Name:        pulumi.String(strings.ReplaceAll(args.Name, "-", "_")),
		Description: pulumi.String("IAM inventory snapshots"),
	}, parent)
	if err != nil {
		return nil, err
	}

	// Columns come straight from the Go row structs the Lambda writes, so schema drift is impossible
	// and the old `make schema` / schemas.json step is gone.
	schemas := transform.Schemas()
	tables := make([]string, 0, len(schemas))
	for t := range schemas {
		tables = append(tables, t)
	}
	sort.Strings(tables)

	for _, table := range tables {
		cols := glue.CatalogTableStorageDescriptorColumnArray{}
		for _, col := range schemas[table] {
			cols = append(cols, &glue.CatalogTableStorageDescriptorColumnArgs{
				Name: pulumi.String(col.Name),
				Type: pulumi.String(col.Type),
			})
		}
		if _, err = glue.NewCatalogTable(ctx, name+"-"+table, &glue.CatalogTableArgs{
			Name:         pulumi.String(table),
			DatabaseName: db.Name,
			TableType:    pulumi.String("EXTERNAL_TABLE"),
			Parameters: pulumi.StringMap{
				"EXTERNAL":                    pulumi.String("TRUE"),
				"classification":              pulumi.String("parquet"),
				"projection.enabled":          pulumi.String("true"),
				"projection.dt.type":          pulumi.String("date"),
				"projection.dt.format":        pulumi.String("yyyy-MM-dd"),
				"projection.dt.range":         pulumi.Sprintf("%s,NOW", args.ProjectionStart),
				"projection.dt.interval":      pulumi.String("1"),
				"projection.dt.interval.unit": pulumi.String("DAYS"),
				"storage.location.template":   pulumi.Sprintf("s3://%s/curated/%s/dt=${dt}/", args.DataBucketName, table),
			},
			PartitionKeys: glue.CatalogTablePartitionKeyArray{
				&glue.CatalogTablePartitionKeyArgs{Name: pulumi.String("dt"), Type: pulumi.String("string")},
			},
			StorageDescriptor: &glue.CatalogTableStorageDescriptorArgs{
				Location:     pulumi.Sprintf("s3://%s/curated/%s/", args.DataBucketName, table),
				InputFormat:  pulumi.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"),
				OutputFormat: pulumi.String("org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"),
				SerDeInfo: &glue.CatalogTableStorageDescriptorSerDeInfoArgs{
					SerializationLibrary: pulumi.String("org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"),
				},
				Columns: cols,
			},
		}, parent); err != nil {
			return nil, fmt.Errorf("table %s: %w", table, err)
		}
	}

	wg, err := athena.NewWorkgroup(ctx, name+"-wg", &athena.WorkgroupArgs{
		Name:         pulumi.String(args.Name),
		ForceDestroy: pulumi.Bool(true),
		Configuration: &athena.WorkgroupConfigurationArgs{
			EnforceWorkgroupConfiguration:   pulumi.Bool(true),
			PublishCloudwatchMetricsEnabled: pulumi.Bool(true),
			BytesScannedCutoffPerQuery:      pulumi.Int(args.ScanLimitBytes),
			ResultConfiguration: &athena.WorkgroupConfigurationResultConfigurationArgs{
				OutputLocation: pulumi.Sprintf("s3://%s/athena/", args.ResultsBucketName),
				EncryptionConfiguration: &athena.WorkgroupConfigurationResultConfigurationEncryptionConfigurationArgs{
					EncryptionOption: pulumi.String("SSE_S3"),
				},
			},
		},
	}, parent)
	if err != nil {
		return nil, err
	}

	const today = "date_format(current_timestamp AT TIME ZONE 'Asia/Kolkata', '%Y-%m-%d')"
	queries := map[string]string{
		"admin-roles-today":           "SELECT role_name, policy_name FROM role_policy_attachments WHERE dt = " + today + " AND policy_name = 'AdministratorAccess'",
		"roles-created-last-7-days":   "SELECT role_name, create_date FROM roles WHERE dt = " + today + " AND create_date > current_timestamp - interval '7' day ORDER BY create_date DESC",
		"findings-today":              "SELECT severity, rule, resource_type, resource_name, detail FROM findings WHERE dt = " + today + " ORDER BY severity, rule",
		"roles-added-since-yesterday": "SELECT t.role_name, t.arn FROM roles t LEFT JOIN roles y ON y.arn = t.arn AND y.dt = date_format(current_timestamp AT TIME ZONE 'Asia/Kolkata' - interval '1' day, '%Y-%m-%d') WHERE t.dt = " + today + " AND y.arn IS NULL",
		"stale-access-keys":           "SELECT user_name, access_key_id, age_days FROM access_keys WHERE dt = " + today + " AND status = 'Active' AND age_days > 90 ORDER BY age_days DESC",
	}
	names := make([]string, 0, len(queries))
	for n := range queries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err = athena.NewNamedQuery(ctx, name+"-q-"+n, &athena.NamedQueryArgs{
			Name:      pulumi.String(n),
			Workgroup: wg.ID(),
			Database:  db.Name,
			Query:     pulumi.String(queries[n]),
		}, parent); err != nil {
			return nil, err
		}
	}

	c.DatabaseName, c.Workgroup = db.Name, wg.Name
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"databaseName": c.DatabaseName, "workgroup": c.Workgroup}); err != nil {
		return nil, err
	}
	return c, nil
}
