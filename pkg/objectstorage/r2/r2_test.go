package r2

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type objectStub struct {
	head      *s3.HeadObjectOutput
	headInput *s3.HeadObjectInput
	deleted   *s3.DeleteObjectInput
}

func (s *objectStub) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	s.headInput = input
	return s.head, nil
}

func (s *objectStub) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	s.deleted = input
	return &s3.DeleteObjectOutput{}, nil
}

type presignStub struct {
	put *s3.PutObjectInput
	get *s3.GetObjectInput
}

func (s *presignStub) PresignPutObject(_ context.Context, input *s3.PutObjectInput, options ...func(*s3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error) {
	s.put = input
	settings := s3.PresignOptions{}
	for _, apply := range options {
		apply(&settings)
	}
	if settings.Expires != 10*time.Minute {
		return nil, errors.New("unexpected upload expiry")
	}
	return &awsv4.PresignedHTTPRequest{
		URL: "https://upload.test", Method: http.MethodPut,
		SignedHeader: http.Header{"Content-Type": []string{"image/png"}},
	}, nil
}

func (s *presignStub) PresignGetObject(_ context.Context, input *s3.GetObjectInput, options ...func(*s3.PresignOptions)) (*awsv4.PresignedHTTPRequest, error) {
	s.get = input
	settings := s3.PresignOptions{}
	for _, apply := range options {
		apply(&settings)
	}
	if settings.Expires != 5*time.Minute {
		return nil, errors.New("unexpected download expiry")
	}
	return &awsv4.PresignedHTTPRequest{
		URL: "https://download.test", Method: http.MethodGet,
	}, nil
}

func testClient() (*Client, *objectStub, *presignStub) {
	objects := &objectStub{}
	presign := &presignStub{}
	return &Client{
		cfg: Config{
			AccountID: "account", AccessKeyID: "key", SecretAccessKey: "secret",
			Bucket: "private", ObjectPrefix: "chat",
			UploadURLExpiry: 10 * time.Minute, DownloadURLExpiry: 5 * time.Minute,
		},
		objects: objects, presign: presign,
	}, objects, presign
}

func TestNewObjectKeyAllowsOnlyConfiguredImageAndVideoTypes(t *testing.T) {
	client, _, _ := testClient()
	key, err := client.NewObjectKey("conversations/conversation-id", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "chat/conversations/conversation-id/") ||
		!strings.HasSuffix(key, ".mp4") {
		t.Fatalf("unexpected key %q", key)
	}
	for _, contentType := range []string{
		"text/html", "image/svg+xml", "application/pdf", "application/octet-stream",
	} {
		if _, err := client.NewObjectKey("conversations/id", contentType); !errors.Is(err, ErrUnsupportedMedia) {
			t.Fatalf("%q should be rejected, got %v", contentType, err)
		}
	}
}

func TestPresignUploadBindsTypeWithoutRequiringContentLength(t *testing.T) {
	client, _, presign := testClient()
	result, err := client.PresignUpload(context.Background(), UploadRequest{
		Key: "chat/conversations/id/object.png", ContentType: "image/png",
		SizeBytes: 1234, MaxSizeBytes: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Method != http.MethodPut ||
		result.Headers["Content-Type"] != "image/png" {
		t.Fatalf("unexpected authorization: %+v", result)
	}
	if _, ok := result.Headers["Content-Length"]; ok {
		t.Fatalf("browser-controlled Content-Length must not be required: %+v", result)
	}
	if aws.ToString(presign.put.Bucket) != "private" ||
		presign.put.ContentLength != nil {
		t.Fatalf("unexpected put request: %+v", presign.put)
	}
}

func TestVerifyUsesAuthoritativeHeadMetadata(t *testing.T) {
	client, objects, _ := testClient()
	modified := time.Now().UTC()
	objects.head = &s3.HeadObjectOutput{
		ContentType:   aws.String("video/mp4"),
		ContentLength: aws.Int64(2048),
		ETag:          aws.String(`"etag"`),
		LastModified:  &modified,
	}
	result, err := client.Verify(context.Background(), VerifyRequest{
		Key:          "chat/conversations/id/object.mp4",
		ExpectedType: "video/mp4", ExpectedSize: 2048, MaxSizeBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ETag != "etag" || result.SizeBytes != 2048 {
		t.Fatalf("unexpected object: %+v", result)
	}
	if _, err := client.Verify(context.Background(), VerifyRequest{
		Key:          "chat/conversations/id/object.mp4",
		ExpectedType: "video/mp4", ExpectedSize: 1024,
	}); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("expected metadata mismatch, got %v", err)
	}
}

func TestNamespaceBoundaryBlocksCrossServiceKeys(t *testing.T) {
	client, _, _ := testClient()
	for _, key := range []string{
		"core/conversations/id/object.png",
		"chat/../core/object.png",
		"/chat/conversations/id/object.png",
	} {
		if _, err := client.Head(context.Background(), key); !errors.Is(err, ErrInvalidObjectKey) {
			t.Fatalf("%q should be rejected, got %v", key, err)
		}
	}
}

func TestDownloadForcesAttachmentDisposition(t *testing.T) {
	client, _, presign := testClient()
	if _, err := client.PresignDownload(
		context.Background(), "chat/conversations/id/object.png", "../../photo.png",
	); err != nil {
		t.Fatal(err)
	}
	disposition := aws.ToString(presign.get.ResponseContentDisposition)
	if !strings.Contains(disposition, "attachment") ||
		!strings.Contains(disposition, "photo.png") ||
		strings.Contains(disposition, "..") {
		t.Fatalf("unsafe content disposition %q", disposition)
	}
}
