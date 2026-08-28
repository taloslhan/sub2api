package repository

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3ClientParams 描述构造 S3 兼容客户端所需的参数。
// 仅暴露兼容客户端构造所需字段，不承载公开 URL、ACL 或整块上传语义。
type S3ClientParams struct {
	Endpoint        string
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

type s3ClientParams = S3ClientParams

// newS3Client 构造一个 S3 兼容客户端，兼容 AWS S3 / Cloudflare R2 / 阿里云 OSS / MinIO。
//
// 通过 SwapComputePayloadSHA256ForUnsignedPayloadMiddleware + RequestChecksumCalculationWhenRequired
// 规避阿里云 OSS 不兼容 s3manager 分片签名的问题（backup 与 image storage 共用此构造）。
func newS3Client(ctx context.Context, p s3ClientParams) (*s3.Client, error) {
	return NewS3Client(ctx, p)
}

// NewS3Client 构造 S3/R2/OSS/MinIO 兼容客户端，供私有归档存储复用。
// CAPYBARA-PATCH: 只提升构造入口可见性，图片与备份现有行为保持不变。
func NewS3Client(ctx context.Context, p S3ClientParams) (*s3.Client, error) {
	region := p.Region
	if region == "" {
		region = "auto" // Cloudflare R2 默认 region
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(p.AccessKeyID, p.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if p.Endpoint != "" {
			o.BaseEndpoint = &p.Endpoint
		}
		if p.ForcePathStyle {
			o.UsePathStyle = true
		}
		o.APIOptions = append(o.APIOptions, v4.SwapComputePayloadSHA256ForUnsignedPayloadMiddleware)
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	}), nil
}
