// Package notify publishes run summaries.
package notify

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

type Notifier interface {
	Notify(ctx context.Context, subject, body string) error
}

// Noop is used when no SNS topic is configured.
type Noop struct{}

func (Noop) Notify(context.Context, string, string) error { return nil }

type SNSAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type SNS struct {
	Client   SNSAPI
	TopicARN string
}

func (s SNS) Notify(ctx context.Context, subject, body string) error {
	if len(subject) > 100 { // SNS limit
		subject = subject[:100]
	}
	_, err := s.Client.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(s.TopicARN), Subject: aws.String(subject), Message: aws.String(body)})
	if err != nil {
		return fmt.Errorf("sns publish: %w", err)
	}
	return nil
}
