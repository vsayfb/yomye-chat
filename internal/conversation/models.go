package conversation

import (
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type Type string

const (
	TypeDirect Type = "direct"
)

type MemberRole string

const (
	RoleMember MemberRole = "member"
	RoleOwner  MemberRole = "owner"
	RoleAdmin  MemberRole = "admin"
)

type Member struct {
	UserID   string     `bson:"user_id" json:"user_id"`
	Role     MemberRole `bson:"role" json:"role"`
	JoinedAt time.Time  `bson:"joined_at" json:"joined_at"`
	LeftAt   *time.Time `bson:"left_at,omitempty" json:"left_at,omitempty"`
}

type Conversation struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`

	Type      Type   `bson:"type" json:"type"`
	DirectKey string `bson:"direct_key,omitempty" json:"-"`
	Title     string `bson:"title,omitempty" json:"title,omitempty"`
	AvatarURL string `bson:"avatar_url,omitempty" json:"avatar_url,omitempty"`
	CreatedBy string `bson:"created_by" json:"created_by"`

	Members []Member `bson:"members" json:"members"`

	LastMessage   string    `bson:"last_message" json:"last_message"`
	LastMessageAt time.Time `bson:"last_message_at" json:"last_message_at"`
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
}

type Message struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`

	ConversationID bson.ObjectID    `bson:"conversation_id" json:"conversation_id"`
	SenderID       string           `bson:"sender_id" json:"sender_id"`
	Content        string           `bson:"content" json:"content"`
	Attachments    []attachment.Ref `bson:"attachments,omitempty" json:"attachments,omitempty"`
	SentAt         time.Time        `bson:"sent_at" json:"sent_at"`
}

type Summary struct {
	ID            bson.ObjectID `json:"id"`
	Type          Type          `json:"type"`
	Members       []Member      `json:"members"`
	LastMessage   string        `json:"last_message"`
	LastMessageAt time.Time     `json:"last_message_at"`
}
