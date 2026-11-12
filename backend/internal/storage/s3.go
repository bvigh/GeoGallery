package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bvigh/geogallery/internal/config"
)

type Client struct {
	s3        *s3.Client
	uploader  *transfermanager.Client
	presigner *s3.PresignClient // signs for cfg.S3PublicEndpoint; never sends a request
	bucket    string
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

	newS3 := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // required for MinIO
		})
	}
	client := newS3(cfg.S3Endpoint)

	// A presigned URL is signed for its host, so it cannot be rewritten afterwards: sign it for the
	// endpoint browsers use. Signing is local, so the backend never has to reach that host itself.
	public := client
	if cfg.S3PublicEndpoint != "" && cfg.S3PublicEndpoint != cfg.S3Endpoint {
		public = newS3(cfg.S3PublicEndpoint)
	}

	return &Client{
		s3:        client,
		uploader:  transfermanager.New(client),
		presigner: s3.NewPresignClient(public),
		bucket:    cfg.S3Bucket,
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

// presignTTL bounds how long a URL stays usable after logout or a user being disabled.
const presignTTL = 15 * time.Minute

func (c *Client) PresignGet(ctx context.Context, key string) (string, error) {
	req, err := c.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(presignTTL))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

// ReadHead returns the object's first n bytes (fewer when the object is shorter) with one ranged
// GET, so re-reading a photo's EXIF never downloads the whole file.
func (c *Client) ReadHead(ctx context.Context, key string, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, fmt.Errorf("read head of %s: n must be positive, got %d", key, n)
	}
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=0-%d", n-1)),
	})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer out.Body.Close()
	b, err := io.ReadAll(io.LimitReader(out.Body, n))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return b, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.s3.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	})
	if err != nil {
		return fmt.Errorf("bucket %s not accessible: %w", c.bucket, err)
	}
	return nil
}
