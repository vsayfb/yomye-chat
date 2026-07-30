package grpcclient

import (
	"testing"

	pb "github.com/vsayfb/gig-platform-protos/contracts"
	"google.golang.org/protobuf/encoding/protowire"
)

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
