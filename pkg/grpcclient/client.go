package grpcclient

import (
	"context"
	"fmt"

	pb "github.com/vsayfb/gig-platform-protos/contracts"
	"google.golang.org/grpc"
)

type UserClient struct {
	client pb.UserServiceClient
}

func NewUserClient(conn *grpc.ClientConn) *UserClient {
	return &UserClient{
		client: pb.NewUserServiceClient(conn),
	}
}

func (c *UserClient) GetUser(ctx context.Context, userID string) (*pb.GetUserResponse, error) {
	resp, err := c.client.GetUser(
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

func (c *UserClient) GetUsers(ctx context.Context, userIDs []string) (*pb.GetUsersResponse, error) {
	resp, err := c.client.GetUsers(
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

func (c *GigClient) GetGig(ctx context.Context, gigID string) (*pb.GetGigResponse, error) {
	resp, err := c.client.GetGig(
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
