package awsclient

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/vsayfb/gig-platform-chat-service/config"
)

func New(ctx context.Context, cfg *config.Config) (aws.Config, error) {
	if cfg.AppEnv != config.EnvironmentDevelopment {
		return awscfg.LoadDefaultConfig(
			ctx,
			awscfg.WithRegion(cfg.AWS.Region),
		)
	}

	return awscfg.LoadDefaultConfig(
		ctx,
		awscfg.WithRegion(cfg.AWS.Region),
		awscfg.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AWS.AccessKeyID,
				cfg.AWS.SecretAccessKey,
				"",
			),
		),
	)
}
