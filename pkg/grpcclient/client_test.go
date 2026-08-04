package grpcclient

import (
	"context"
	"testing"

	pb "github.com/vsayfb/gig-platform-protos/contracts"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
)

type fakeUserServiceClient struct {
	getUserCalled bool
	users         []*pb.User
}

func (f *fakeUserServiceClient) GetUser(
	context.Context,
	*pb.GetUserRequest,
	...grpc.CallOption,
) (*pb.GetUserResponse, error) {
	f.getUserCalled = true
	return &pb.GetUserResponse{}, nil
}

func (f *fakeUserServiceClient) GetUsers(
	_ context.Context,
	_ *pb.GetUsersRequest,
	_ ...grpc.CallOption,
) (*pb.GetUsersResponse, error) {
	return &pb.GetUsersResponse{Users: f.users}, nil
}

func TestAllowsSocialDirectMessagesUsesSummaryLookup(t *testing.T) {
	t.Parallel()

	const targetID = "target-id"
	user := &pb.User{Id: targetID}
	data := protowire.AppendTag(nil, 4, protowire.VarintType)
	data = protowire.AppendVarint(data, 1)
	user.ProtoReflect().SetUnknown(data)
	fake := &fakeUserServiceClient{users: []*pb.User{user}}
	client := &UserClient{client: fake}

	allowed, err := client.AllowsSocialDirectMessages(context.Background(), targetID)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("enabled social direct-message preference was rejected")
	}
	if fake.getUserCalled {
		t.Fatal("policy lookup must use GetUsers until Core GetUser exposes field 4")
	}
}

func TestAllowsSocialDirectMessagesRejectsMismatchedUser(t *testing.T) {
	t.Parallel()

	client := &UserClient{client: &fakeUserServiceClient{
		users: []*pb.User{{Id: "different-user"}},
	}}
	allowed, err := client.AllowsSocialDirectMessages(context.Background(), "target-id")
	if err == nil || allowed {
		t.Fatalf("mismatched response must fail closed: allowed=%t err=%v", allowed, err)
	}
}

func TestAllowsSocialDirectMessagesReadsAdditiveField(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		value uint64
		want  bool
	}{
		{name: "enabled", value: 1, want: true},
		{name: "disabled", value: 0, want: false},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			user := &pb.User{}
			data := protowire.AppendTag(nil, 4, protowire.VarintType)
			data = protowire.AppendVarint(data, test.value)
			user.ProtoReflect().SetUnknown(data)

			got, err := allowsSocialDirectMessages(user)
			if err != nil {
				t.Fatalf("allowsSocialDirectMessages() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("allowsSocialDirectMessages() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestAllowsSocialDirectMessagesDefaultsToDisabled(t *testing.T) {
	t.Parallel()

	got, err := allowsSocialDirectMessages(&pb.User{})
	if err != nil {
		t.Fatalf("allowsSocialDirectMessages() error = %v", err)
	}
	if got {
		t.Fatal("missing preference field must default to disabled")
	}
}
