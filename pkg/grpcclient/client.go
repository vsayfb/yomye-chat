package grpcclient

import (
	"context"
	"fmt"
	"time"

	chatTracing "github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
	pb "github.com/vsayfb/gig-platform-protos/contracts"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
)

const coreRPCTimeout = 3 * time.Second

func withCoreRPCTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, coreRPCTimeout)
}

type UserClient struct {
	client pb.UserServiceClient
}

func NewUserClient(conn *grpc.ClientConn) *UserClient {
	return &UserClient{
		client: pb.NewUserServiceClient(conn),
	}
}

func (c *UserClient) GetUser(ctx context.Context, userID string) (resp *pb.GetUserResponse, err error) {
	ctx, cancel := withCoreRPCTimeout(ctx)
	defer cancel()
	ctx, span := chatTracing.StartDomain(ctx, "core.user.get",
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "UserService"),
		attribute.String("rpc.method", "GetUser"),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	resp, err = c.client.GetUser(
		ctx,
		&pb.GetUserRequest{
			UserId: userID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("grpc GetUser: %w", err)
	}

	return resp, nil
}

func (c *UserClient) AllowsSocialDirectMessages(
	ctx context.Context,
	userID string,
) (bool, error) {
	resp, err := c.GetUsers(ctx, []string{userID})
	if err != nil {
		return false, err
	}
	if len(resp.GetUsers()) != 1 || resp.GetUsers()[0].GetId() != userID {
		return false, fmt.Errorf("grpc GetUsers: target user missing or mismatched")
	}
	return allowsSocialDirectMessages(resp.GetUsers()[0])
}

// allowsSocialDirectMessages reads field 4 from the protobuf wire data. The
// stable generated client predates that additive field, but protobuf preserves
// it as unknown data. This can be replaced with the generated getter once the
// renamed contracts module is regenerated with one consistent Go package.
func allowsSocialDirectMessages(user *pb.User) (bool, error) {
	data := user.ProtoReflect().GetUnknown()
	for len(data) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return false, fmt.Errorf("decode user preference tag: %v", protowire.ParseError(tagLength))
		}
		data = data[tagLength:]

		if number == 4 {
			if wireType != protowire.VarintType {
				return false, fmt.Errorf("decode user preference: unexpected wire type %v", wireType)
			}
			value, valueLength := protowire.ConsumeVarint(data)
			if valueLength < 0 {
				return false, fmt.Errorf(
					"decode user preference value: %v",
					protowire.ParseError(valueLength),
				)
			}
			return value != 0, nil
		}

		fieldLength := protowire.ConsumeFieldValue(number, wireType, data)
		if fieldLength < 0 {
			return false, fmt.Errorf(
				"decode user preference field: %v",
				protowire.ParseError(fieldLength),
			)
		}
		data = data[fieldLength:]
	}

	// Missing field follows protobuf's bool default and fails closed.
	return false, nil
}

func (c *UserClient) GetUsers(ctx context.Context, userIDs []string) (resp *pb.GetUsersResponse, err error) {
	ctx, cancel := withCoreRPCTimeout(ctx)
	defer cancel()
	ctx, span := chatTracing.StartDomain(ctx, "core.user.list",
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "UserService"),
		attribute.String("rpc.method", "GetUsers"),
		attribute.Int("rpc.request_count", len(userIDs)),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	resp, err = c.client.GetUsers(
		ctx,
		&pb.GetUsersRequest{
			UserIds: userIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("grpc GetUsers: %w", err)
	}

	return resp, nil
}

type GigClient struct {
	client pb.GigServiceClient
}

func NewGigClient(conn *grpc.ClientConn) *GigClient {
	return &GigClient{
		client: pb.NewGigServiceClient(conn),
	}
}

func (c *GigClient) GetGig(ctx context.Context, gigID string) (resp *pb.GetGigResponse, err error) {
	ctx, cancel := withCoreRPCTimeout(ctx)
	defer cancel()
	ctx, span := chatTracing.StartDomain(ctx, "core.gig.get",
		attribute.String("rpc.system", "grpc"),
		attribute.String("rpc.service", "GigService"),
		attribute.String("rpc.method", "GetGig"),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	resp, err = c.client.GetGig(
		ctx,
		&pb.GetGigRequest{
			GigId: gigID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("grpc GetGig: %w", err)
	}

	return resp, nil
}
