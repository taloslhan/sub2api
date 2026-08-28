package sessionarchive

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type ExportTicket struct {
	ID         string
	AdminID    int64
	Format     string
	SessionIDs []int64
	Filter     SessionFilter
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type TicketStore interface {
	Put(context.Context, ExportTicket, time.Duration) error
	Consume(context.Context, string) (ExportTicket, error)
}

type RedisTicketStore struct {
	client redis.UniversalClient
	prefix string
}

func NewRedisTicketStore(client redis.UniversalClient, prefix string) (*RedisTicketStore, error) {
	prefix = strings.TrimSpace(prefix)
	if client == nil || prefix == "" {
		return nil, errors.New("redis ticket store requires client and prefix")
	}
	return &RedisTicketStore{client: client, prefix: prefix}, nil
}

func (s *RedisTicketStore) Put(ctx context.Context, ticket ExportTicket, ttl time.Duration) error {
	payload, err := json.Marshal(ticket)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, s.prefix+ticket.ID, payload, ttl).Err()
}

func (s *RedisTicketStore) Consume(ctx context.Context, id string) (ExportTicket, error) {
	payload, err := s.client.GetDel(ctx, s.prefix+id).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ExportTicket{}, errors.New("export ticket is missing, expired, or already consumed")
		}
		return ExportTicket{}, err
	}
	var ticket ExportTicket
	if err := json.Unmarshal(payload, &ticket); err != nil {
		return ExportTicket{}, err
	}
	if ticket.ID != id || time.Now().After(ticket.ExpiresAt) {
		return ExportTicket{}, errors.New("export ticket is invalid or expired")
	}
	return ticket, nil
}
