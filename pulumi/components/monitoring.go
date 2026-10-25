package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type MonitoringArgs struct {
	Name             string
	FunctionName     pulumi.StringInput
	DlqName          pulumi.StringInput
	SnsTopicArn      pulumi.StringInput
	MetricNamespace  string // defaults to IAMInventory
	ServiceDimension string // defaults to iam-inventory
	StaleAfterHours  int    // defaults to 26
}

// Monitoring holds the three alarms (errors, no recent success, DLQ not empty) and the dashboard.
type Monitoring struct {
	pulumi.ResourceState

	DashboardName pulumi.StringOutput
}

func NewMonitoring(ctx *pulumi.Context, name string, args MonitoringArgs, opts ...pulumi.ResourceOption) (*Monitoring, error) {
	if args.MetricNamespace == "" {
		args.MetricNamespace = "IAMInventory"
	}
	if args.ServiceDimension == "" {
		args.ServiceDimension = "iam-inventory"
	}
	if args.StaleAfterHours == 0 {
		args.StaleAfterHours = 26
	}
	c := &Monitoring{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:Monitoring", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)
	topic := pulumi.Array{args.SnsTopicArn}

	if _, err := cloudwatch.NewMetricAlarm(ctx, name+"-errors", &cloudwatch.MetricAlarmArgs{
		Name:               pulumi.Sprintf("%s-lambda-errors", args.Name),
		AlarmDescription:   pulumi.String("The IAM inventory Lambda raised an error."),
		Namespace:          pulumi.String("AWS/Lambda"),
		MetricName:         pulumi.String("Errors"),
		Dimensions:         pulumi.StringMap{"FunctionName": args.FunctionName},
		Statistic:          pulumi.String("Sum"),
		Period:             pulumi.Int(300),
		EvaluationPeriods:  pulumi.Int(1),
		Threshold:          pulumi.Float64(0),
		ComparisonOperator: pulumi.String("GreaterThanThreshold"),
		TreatMissingData:   pulumi.String("notBreaching"),
		AlarmActions:       topic,
		OkActions:          topic,
	}, parent); err != nil {
		return nil, err
	}

	// run_success is emitted by the function itself (CloudWatch EMF): 1 per good run, 0 per failed run.
	// Hourly buckets with no data count as breaching, so N consecutive hours without a success trips it.
	if _, err := cloudwatch.NewMetricAlarm(ctx, name+"-no-success", &cloudwatch.MetricAlarmArgs{
		Name:               pulumi.Sprintf("%s-no-successful-run", args.Name),
		AlarmDescription:   pulumi.Sprintf("No successful run in %d hours.", args.StaleAfterHours),
		Namespace:          pulumi.String(args.MetricNamespace),
		MetricName:         pulumi.String("run_success"),
		Dimensions:         pulumi.StringMap{"Service": pulumi.String(args.ServiceDimension)},
		Statistic:          pulumi.String("Sum"),
		Period:             pulumi.Int(3600),
		EvaluationPeriods:  pulumi.Int(args.StaleAfterHours),
		DatapointsToAlarm:  pulumi.Int(args.StaleAfterHours),
		Threshold:          pulumi.Float64(1),
		ComparisonOperator: pulumi.String("LessThanThreshold"),
		TreatMissingData:   pulumi.String("breaching"),
		AlarmActions:       topic,
		OkActions:          topic,
	}, parent); err != nil {
		return nil, err
	}

	if _, err := cloudwatch.NewMetricAlarm(ctx, name+"-dlq", &cloudwatch.MetricAlarmArgs{
		Name:               pulumi.Sprintf("%s-dlq-not-empty", args.Name),
		AlarmDescription:   pulumi.String("A failed invocation landed in the dead-letter queue."),
		Namespace:          pulumi.String("AWS/SQS"),
		MetricName:         pulumi.String("ApproximateNumberOfMessagesVisible"),
		Dimensions:         pulumi.StringMap{"QueueName": args.DlqName},
		Statistic:          pulumi.String("Maximum"),
		Period:             pulumi.Int(300),
		EvaluationPeriods:  pulumi.Int(1),
		Threshold:          pulumi.Float64(0),
		ComparisonOperator: pulumi.String("GreaterThanThreshold"),
		TreatMissingData:   pulumi.String("notBreaching"),
		AlarmActions:       topic,
	}, parent); err != nil {
		return nil, err
	}

	region := aws.GetRegionOutput(ctx, aws.GetRegionOutputArgs{}, pulumi.Parent(c)).Region()
	metric := func(metricName string) pulumi.Array {
		return pulumi.Array{pulumi.String(args.MetricNamespace), pulumi.String(metricName), pulumi.String("Service"), pulumi.String(args.ServiceDimension)}
	}
	widget := func(x, y, w int, title, stat string, period int, metrics ...pulumi.Array) pulumi.Map {
		ms := pulumi.Array{}
		for _, m := range metrics {
			ms = append(ms, m)
		}
		return pulumi.Map{
			"type": pulumi.String("metric"), "x": pulumi.Int(x), "y": pulumi.Int(y), "width": pulumi.Int(w), "height": pulumi.Int(6),
			"properties": pulumi.Map{
				"title": pulumi.String(title), "region": region, "stat": pulumi.String(stat),
				"period": pulumi.Int(period), "metrics": ms,
			},
		}
	}
	lambdaErrors := pulumi.Array{pulumi.String("AWS/Lambda"), pulumi.String("Errors"), pulumi.String("FunctionName"), args.FunctionName}

	dash, err := cloudwatch.NewDashboard(ctx, name+"-dashboard", &cloudwatch.DashboardArgs{
		DashboardName: pulumi.String(args.Name),
		DashboardBody: pulumi.JSONMarshal(pulumi.Map{"widgets": pulumi.Array{
			widget(0, 0, 12, "Inventory size", "Maximum", 86400,
				metric("roles_collected"), metric("users_collected"), metric("groups_collected"), metric("policies_collected")),
			widget(12, 0, 12, "Findings and changes", "Maximum", 86400,
				metric("findings_count"), metric("critical_high_findings"), metric("changes_count")),
			widget(0, 6, 24, "Run health", "Sum", 3600, metric("run_success"), lambdaErrors),
		}}),
	}, parent)
	if err != nil {
		return nil, err
	}

	c.DashboardName = dash.DashboardName
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"dashboardName": c.DashboardName}); err != nil {
		return nil, err
	}
	return c, nil
}
