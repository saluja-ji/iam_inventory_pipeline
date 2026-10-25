package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type LambdaArgs struct {
	FunctionName     string
	SourceDir        string // directory holding the `bootstrap` binary; produced by `make package`
	DataBucketArn    pulumi.StringInput
	DataBucketName   pulumi.StringInput
	SnsTopicArn      pulumi.StringInput
	LogRetentionDays int
	MemorySize       int
	Timeout          int
	AlertMinSeverity string
	AdotLayerArn     string // empty disables OpenTelemetry export
	ExtraEnvironment map[string]string
}

func (a *LambdaArgs) defaults() {
	if a.LogRetentionDays == 0 {
		a.LogRetentionDays = 30
	}
	if a.MemorySize == 0 {
		a.MemorySize = 1024
	}
	if a.Timeout == 0 {
		a.Timeout = 300
	}
	if a.AlertMinSeverity == "" {
		a.AlertMinSeverity = "high"
	}
}

// Lambda is the collector function with its log group, DLQ and least-privilege execution role.
type Lambda struct {
	pulumi.ResourceState

	FunctionName pulumi.StringOutput
	FunctionArn  pulumi.StringOutput
	RoleArn      pulumi.StringOutput
	DlqName      pulumi.StringOutput
	DlqArn       pulumi.StringOutput
	LogGroupName pulumi.StringOutput
}

func NewLambda(ctx *pulumi.Context, name string, args LambdaArgs, opts ...pulumi.ResourceOption) (*Lambda, error) {
	args.defaults()
	c := &Lambda{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:Lambda", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)
	otelEnabled := args.AdotLayerArn != ""

	env := pulumi.StringMap{
		"DATA_BUCKET":        args.DataBucketName,
		"SNS_TOPIC_ARN":      args.SnsTopicArn,
		"ALERT_MIN_SEVERITY": pulumi.String(args.AlertMinSeverity),
		"OTEL_SERVICE_NAME":  pulumi.String("iam-inventory"),
	}
	if otelEnabled {
		env["OTEL_EXPORTER_OTLP_ENDPOINT"] = pulumi.String("http://localhost:4318")
		env["OTEL_EXPORTER_OTLP_PROTOCOL"] = pulumi.String("http/protobuf")
		env["OPENTELEMETRY_COLLECTOR_CONFIG_URI"] = pulumi.String("/var/task/collector.yaml")
		env["OPENTELEMETRY_COLLECTOR_CONFIG_FILE"] = pulumi.String("/var/task/collector.yaml")
	}
	for k, v := range args.ExtraEnvironment {
		env[k] = pulumi.String(v)
	}

	logGroup, err := cloudwatch.NewLogGroup(ctx, name+"-logs", &cloudwatch.LogGroupArgs{
		Name:            pulumi.Sprintf("/aws/lambda/%s", args.FunctionName),
		RetentionInDays: pulumi.Int(args.LogRetentionDays),
	}, parent)
	if err != nil {
		return nil, err
	}

	dlq, err := sqs.NewQueue(ctx, name+"-dlq", &sqs.QueueArgs{
		Name:                    pulumi.Sprintf("%s-dlq", args.FunctionName),
		MessageRetentionSeconds: pulumi.Int(1209600),
		SqsManagedSseEnabled:    pulumi.Bool(true),
	}, parent)
	if err != nil {
		return nil, err
	}

	role, err := iam.NewRole(ctx, name+"-exec", &iam.RoleArgs{
		Name:             pulumi.Sprintf("%s-exec", args.FunctionName),
		AssumeRolePolicy: servicePrincipalTrust("lambda.amazonaws.com"),
	}, parent)
	if err != nil {
		return nil, err
	}

	stmt := func(sid string, actions []string, resources ...pulumi.Input) pulumi.Map {
		as := pulumi.StringArray{}
		for _, a := range actions {
			as = append(as, pulumi.String(a))
		}
		return pulumi.Map{
			"Sid":      pulumi.String(sid),
			"Effect":   pulumi.String("Allow"),
			"Action":   as,
			"Resource": pulumi.Array(resources),
		}
	}
	statements := pulumi.Array{
		// the API does not support resource-level permissions
		stmt("ReadIamAccountDetails", []string{"iam:GetAccountAuthorizationDetails"}, pulumi.String("*")),
		stmt("ReadUserAccessKeyMetadata", []string{"iam:ListAccessKeys"}, pulumi.String("arn:aws:iam::*:user/*")),
		stmt("DataBucketObjects", []string{"s3:PutObject", "s3:GetObject"},
			pulumi.Sprintf("%s/raw/*", args.DataBucketArn),
			pulumi.Sprintf("%s/curated/*", args.DataBucketArn),
			pulumi.Sprintf("%s/findings/*", args.DataBucketArn),
			pulumi.Sprintf("%s/diff/*", args.DataBucketArn),
		),
		// Without ListBucket, S3 answers a missing key with 403 instead of 404, breaking "no previous snapshot".
		stmt("DataBucketList", []string{"s3:ListBucket"}, args.DataBucketArn),
		stmt("Logs", []string{"logs:CreateLogStream", "logs:PutLogEvents"}, pulumi.Sprintf("%s:*", logGroup.Arn)),
		stmt("Alerts", []string{"sns:Publish"}, args.SnsTopicArn),
		stmt("DeadLetter", []string{"sqs:SendMessage"}, dlq.Arn),
	}
	if otelEnabled {
		statements = append(statements,
			stmt("XRayExport", []string{"xray:PutTraceSegments", "xray:PutTelemetryRecords"}, pulumi.String("*")),
			// The collector's awsemf exporter writes OTel metrics into the function's own log group.
			stmt("CollectorEmfLogs", []string{"logs:CreateLogGroup", "logs:DescribeLogGroups", "logs:DescribeLogStreams"},
				logGroup.Arn, pulumi.Sprintf("%s:*", logGroup.Arn)),
		)
	}

	policy, err := iam.NewRolePolicy(ctx, name+"-policy", &iam.RolePolicyArgs{
		Name: pulumi.String("least-privilege"),
		Role: role.ID(),
		Policy: pulumi.JSONMarshal(pulumi.Map{
			"Version":   pulumi.String("2012-10-17"),
			"Statement": statements,
		}),
	}, parent)
	if err != nil {
		return nil, err
	}

	layers := pulumi.StringArray{}
	tracing := "PassThrough"
	if otelEnabled {
		layers = append(layers, pulumi.String(args.AdotLayerArn))
		tracing = "Active"
	}

	fn, err := lambda.NewFunction(ctx, name, &lambda.FunctionArgs{
		Name:          pulumi.String(args.FunctionName),
		Role:          role.Arn,
		Runtime:       pulumi.String("provided.al2023"),
		Architectures: pulumi.StringArray{pulumi.String("arm64")},
		Handler:       pulumi.String("bootstrap"),
		// A directory FileArchive is zipped by Pulumi, which also tracks its content hash.
		Code:        pulumi.NewFileArchive(args.SourceDir),
		MemorySize:  pulumi.Int(args.MemorySize),
		Timeout:     pulumi.Int(args.Timeout),
		Layers:      layers,
		Environment: &lambda.FunctionEnvironmentArgs{Variables: env},
		DeadLetterConfig: &lambda.FunctionDeadLetterConfigArgs{
			TargetArn: dlq.Arn,
		},
		TracingConfig: &lambda.FunctionTracingConfigArgs{Mode: pulumi.String(tracing)},
	}, parent, pulumi.DependsOn([]pulumi.Resource{logGroup, policy}))
	if err != nil {
		return nil, err
	}

	c.FunctionName, c.FunctionArn, c.RoleArn = fn.Name, fn.Arn, role.Arn
	c.DlqName, c.DlqArn, c.LogGroupName = dlq.Name, dlq.Arn, logGroup.Name
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{
		"functionName": c.FunctionName,
		"functionArn":  c.FunctionArn,
	}); err != nil {
		return nil, err
	}
	return c, nil
}
