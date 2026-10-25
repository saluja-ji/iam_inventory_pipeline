package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type SchedulerArgs struct {
	Name      string
	LambdaArn pulumi.StringInput
	Cron      string // EventBridge Scheduler cron, e.g. "cron(0 18 * * ? *)"
	Timezone  string
}

// Scheduler invokes the Lambda on a cron expression in a named time zone.
type Scheduler struct {
	pulumi.ResourceState

	ScheduleArn pulumi.StringOutput
}

func NewScheduler(ctx *pulumi.Context, name string, args SchedulerArgs, opts ...pulumi.ResourceOption) (*Scheduler, error) {
	c := &Scheduler{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:Scheduler", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)

	role, err := iam.NewRole(ctx, name+"-invoke", &iam.RoleArgs{
		Name:             pulumi.Sprintf("%s-scheduler-invoke", args.Name),
		AssumeRolePolicy: servicePrincipalTrust("scheduler.amazonaws.com"),
	}, parent)
	if err != nil {
		return nil, err
	}

	if _, err = iam.NewRolePolicy(ctx, name+"-invoke", &iam.RolePolicyArgs{
		Name: pulumi.String("invoke-lambda"),
		Role: role.ID(),
		Policy: pulumi.JSONMarshal(pulumi.Map{
			"Version": pulumi.String("2012-10-17"),
			"Statement": pulumi.Array{pulumi.Map{
				"Effect":   pulumi.String("Allow"),
				"Action":   pulumi.String("lambda:InvokeFunction"),
				"Resource": args.LambdaArn,
			}},
		}),
	}, parent); err != nil {
		return nil, err
	}

	s, err := scheduler.NewSchedule(ctx, name, &scheduler.ScheduleArgs{
		Name:                       pulumi.String(args.Name),
		Description:                pulumi.String("Daily IAM inventory snapshot"),
		ScheduleExpression:         pulumi.String(args.Cron),
		ScheduleExpressionTimezone: pulumi.String(args.Timezone),
		State:                      pulumi.String("ENABLED"),
		FlexibleTimeWindow:         &scheduler.ScheduleFlexibleTimeWindowArgs{Mode: pulumi.String("OFF")},
		Target: &scheduler.ScheduleTargetArgs{
			Arn:     args.LambdaArn,
			RoleArn: role.Arn,
			Input:   pulumi.JSONMarshal(pulumi.Map{"source": pulumi.String("eventbridge-scheduler")}),
			RetryPolicy: &scheduler.ScheduleTargetRetryPolicyArgs{
				MaximumRetryAttempts:     pulumi.Int(2),
				MaximumEventAgeInSeconds: pulumi.Int(3600),
			},
		},
	}, parent)
	if err != nil {
		return nil, err
	}

	c.ScheduleArn = s.Arn
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"scheduleArn": c.ScheduleArn}); err != nil {
		return nil, err
	}
	return c, nil
}

// servicePrincipalTrust is the assume-role policy for an AWS service principal.
func servicePrincipalTrust(service string) pulumi.StringOutput {
	return pulumi.JSONMarshal(pulumi.Map{
		"Version": pulumi.String("2012-10-17"),
		"Statement": pulumi.Array{pulumi.Map{
			"Effect":    pulumi.String("Allow"),
			"Action":    pulumi.String("sts:AssumeRole"),
			"Principal": pulumi.Map{"Service": pulumi.String(service)},
		}},
	})
}
