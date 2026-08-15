// Package r2 provides domain-free primitives for private Cloudflare R2
// objects. Authorization decisions remain the responsibility of the calling
// service.
package r2

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"
	chatTracing "github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
	"go.opentelemetry.io/otel/attribute"
)

var (
	ErrInvalidConfig    = errors.New("invalid R2 configuration")
	ErrInvalidObjectKey = errors.New("invalid R2 object key")
	ErrUnsupportedMedia = errors.New("unsupported media type")
	ErrInvalidObject    = errors.New("invalid R2 object")
)

type Config struct {
	AccountID         string
	AccessKeyID       string
	SecretAccessKey   string
	Bucket            string
	ObjectPrefix      string
	UploadURLExpiry   time.Duration
	DownloadURLExpiry time.Duration
}

type UploadAuthorization struct {
	Key       string            `json:"key"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type DownloadAuthorization struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Object struct {
	Key          string
	ContentType  string
	SizeBytes    int64
	ETag         string
	LastModified *time.Time
}

type UploadRequest struct {
	Key          string
	ContentType  string
	SizeBytes    int64
	MaxSizeBytes int64
}

type VerifyRequest struct {
	Key          string
	ExpectedType string
	ExpectedSize int64
	MaxSizeBytes int64
}

type objectClient interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type presignClient interface {
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error)
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error)
}

type Client struct {
	cfg     Config
	objects objectClient
	presign presignClient
}

func New(ctx context.Context, cfg Config) (*Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
	awsConfig, err := awscfg.LoadDefaultConfig(
		ctx,
		awscfg.WithRegion("auto"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("load R2 client configuration: %w", err)
	}
	s3Client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	return &Client{
		cfg: cfg, objects: s3Client, presign: s3.NewPresignClient(s3Client),
	}, nil
}

func (c *Client) NewObjectKey(scope, contentType string) (string, error) {
	scope = strings.Trim(strings.TrimSpace(scope), "/")
	if !validPath(scope) {
		return "", ErrInvalidObjectKey
	}
	extension, ok := allowedMediaTypes[normalizeContentType(contentType)]
	if !ok {
		return "", ErrUnsupportedMedia
	}
	return path.Join(c.cfg.ObjectPrefix, scope, uuid.NewString()+extension), nil
}

func (c *Client) PresignUpload(
	ctx context.Context,
	request UploadRequest,
) (*UploadAuthorization, error) {
	if err := c.validateKey(request.Key); err != nil {
		return nil, err
	}
	contentType := normalizeContentType(request.ContentType)
	if _, ok := allowedMediaTypes[contentType]; !ok {
		return nil, ErrUnsupportedMedia
	}
	if request.SizeBytes <= 0 || request.MaxSizeBytes <= 0 ||
		request.SizeBytes > request.MaxSizeBytes {
		return nil, fmt.Errorf("%w: size is outside the allowed range", ErrInvalidObject)
	}
	expiresAt := time.Now().UTC().Add(c.cfg.UploadURLExpiry)
	result, err := c.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.cfg.Bucket),
		Key:         aws.String(request.Key),
		ContentType: aws.String(contentType),
		IfNoneMatch: aws.String("*"),
	}, func(options *s3.PresignOptions) {
		options.Expires = c.cfg.UploadURLExpiry
	})
	if err != nil {
		return nil, fmt.Errorf("presign R2 upload: %w", err)
	}
	headers := map[string]string{
		"Content-Type":  contentType,
		"If-None-Match": "*",
	}
	for name, values := range result.SignedHeader {
		if !strings.EqualFold(name, "host") {
			headers[name] = strings.Join(values, ",")
		}
	}
	return &UploadAuthorization{
		Key: request.Key, URL: result.URL, Method: result.Method,
		Headers: headers, ExpiresAt: expiresAt,
	}, nil
}

func (c *Client) PresignDownload(
	ctx context.Context,
	key, downloadName string,
) (*DownloadAuthorization, error) {
	if err := c.validateKey(key); err != nil {
		return nil, err
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	}
	if name := safeDownloadName(downloadName); name != "" {
		input.ResponseContentDisposition = aws.String(
			mime.FormatMediaType("attachment", map[string]string{"filename": name}),
		)
	}
	result, err := c.presign.PresignGetObject(ctx, input, func(options *s3.PresignOptions) {
		options.Expires = c.cfg.DownloadURLExpiry
	})
	if err != nil {
		return nil, fmt.Errorf("presign R2 download: %w", err)
	}
	return &DownloadAuthorization{
		URL: result.URL, ExpiresAt: time.Now().UTC().Add(c.cfg.DownloadURLExpiry),
	}, nil
}

func (c *Client) Head(ctx context.Context, key string) (object *Object, err error) {
	ctx, span := chatTracing.StartDomain(ctx, "r2.object.head",
		attribute.String("storage.system", "cloudflare_r2"),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	if err := c.validateKey(key); err != nil {
		return nil, err
	}
	result, err := c.objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("head R2 object: %w", err)
	}
	return &Object{
		Key: key, ContentType: normalizeContentType(aws.ToString(result.ContentType)),
		SizeBytes:    aws.ToInt64(result.ContentLength),
		ETag:         strings.Trim(aws.ToString(result.ETag), `"`),
		LastModified: result.LastModified,
	}, nil
}

func (c *Client) Verify(ctx context.Context, request VerifyRequest) (*Object, error) {
	object, err := c.Head(ctx, request.Key)
	if err != nil {
		return nil, err
	}
	expectedType := normalizeContentType(request.ExpectedType)
	if _, ok := allowedMediaTypes[expectedType]; !ok {
		return nil, ErrUnsupportedMedia
	}
	if object.ContentType != expectedType ||
		object.SizeBytes <= 0 ||
		(request.ExpectedSize > 0 && object.SizeBytes != request.ExpectedSize) ||
		(request.MaxSizeBytes > 0 && object.SizeBytes > request.MaxSizeBytes) {
		return nil, ErrInvalidObject
	}
	return object, nil
}

func (c *Client) Delete(ctx context.Context, key string) (err error) {
	ctx, span := chatTracing.StartDomain(ctx, "r2.object.delete",
		attribute.String("storage.system", "cloudflare_r2"),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	if err := c.validateKey(key); err != nil {
		return err
	}
	_, err = c.objects.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil
		}
		return fmt.Errorf("delete R2 object: %w", err)
	}
	return nil
}

func (c *Client) validateKey(key string) error {
	if !validPath(key) || !strings.HasPrefix(key, c.cfg.ObjectPrefix+"/") {
		return ErrInvalidObjectKey
	}
	return nil
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.AccountID) == "" ||
		strings.TrimSpace(cfg.AccessKeyID) == "" ||
		strings.TrimSpace(cfg.SecretAccessKey) == "" ||
		strings.TrimSpace(cfg.Bucket) == "" ||
		!validPath(cfg.ObjectPrefix) ||
		cfg.UploadURLExpiry <= 0 || cfg.UploadURLExpiry > time.Hour ||
		cfg.DownloadURLExpiry <= 0 || cfg.DownloadURLExpiry > time.Hour {
		return ErrInvalidConfig
	}
	return nil
}

func validPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." ||
			url.PathEscape(segment) != segment {
			return false
		}
	}
	return true
}

func safeDownloadName(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = path.Base(value)
	if value == "." || value == "/" || value == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func normalizeContentType(value string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(mediaType)
}

var allowedMediaTypes = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
	"video/webm":      ".webm",
}
