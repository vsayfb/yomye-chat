package sqs

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/vsayfb/gig-platform-chat-service/config"
)

func NewSQS(cfg aws.Config, appCfg *config.Config) *sqs.Client {
	if appCfg.AppEnv == config.EnvironmentProduction {
		return sqs.NewFromConfig(cfg)

	}

	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(appCfg.AWS.SQSEndpoint)
	})
}
