package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bvigh/geogallery/internal/config"
)

type Client struct {
	s3       *s3.Client
	uploader *transfermanager.Client
	bucket   string
}

func NewClient(cfg *config.Config) (*Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKey, cfg.S3SecretKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		o.UsePathStyle = true // required for MinIO
	})

	return &Client{
		s3:       client,
		uploader: transfermanager.New(client),
		bucket:   cfg.S3Bucket,
	}, nil
}

// Upload streams body to S3 using the transfer manager, which handles
// non-seekable readers without buffering the whole file in memory.
func (c *Client) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := c.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: &size,
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	return nil
}

func (c *Client) PresignGet(ctx context.Context, key string) (string, error) {
	presigner := s3.NewPresignClient(c.s3)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}
