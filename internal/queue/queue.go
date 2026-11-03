package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

type Priority int

const (
	PriorityLow Priority = iota
	PriorityHigh
)

type Job struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	ChatID    int64     `json:"chat_id"`
	Priority  Priority  `json:"priority"`
	Quality   string    `json:"quality"`    // "best", "1080p", "720p", "480p", "360p", "audio"
	MediaType string    `json:"media_type"` // "video" or "audio"
	CreatedAt time.Time `json:"created_at"`

	IsPlaylist bool   `json:"is_playlist,omitempty"` // URL is a whole YouTube playlist
	Delivery   string `json:"delivery,omitempty"`    // playlists: "sequential" or "zip"
}

type Queue interface {
	Enqueue(job *Job) error
	Dequeue(ctx context.Context) (*Job, error)
	GetStatus() int
	Close() error
	GetClient() *redis.Client // For accessing Redis client for preferences
}

type RedisQueue struct {
	client *redis.Client
	ctx    context.Context
}

func NewRedisQueue(addr string) (*RedisQueue, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		MaxRetries:   3,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	ctx := context.Background()

	// Test connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	// Configure Redis for ARM (ignore errors if not supported)
	client.ConfigSet(ctx, "maxmemory", "1gb")
	client.ConfigSet(ctx, "save", "")
	client.ConfigSet(ctx, "activerehashing", "yes")
	// Note: ConfigSet errors are ignored as these are optimizations, not requirements

	return &RedisQueue{
		client: client,
		ctx:    ctx,
	}, nil
}

func (q *RedisQueue) Enqueue(job *Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	queueName := "queue:low"
	if job.Priority == PriorityHigh {
		queueName = "queue:high"
	}

	return q.client.LPush(q.ctx, queueName, data).Err()
}

// GetClient returns the underlying Redis client (for preferences storage)
func (q *RedisQueue) GetClient() *redis.Client {
	return q.client
}

func (q *RedisQueue) Dequeue(ctx context.Context) (*Job, error) {
	// First try high priority queue, then low priority
	queues := []string{"queue:high", "queue:low"}

	for _, queueName := range queues {
		result, err := q.client.BRPop(ctx, 1*time.Second, queueName).Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to dequeue: %w", err)
		}

		if len(result) < 2 {
			continue
		}

		var job Job
		if err := json.Unmarshal([]byte(result[1]), &job); err != nil {
			return nil, fmt.Errorf("failed to unmarshal job: %w", err)
		}

		return &job, nil
	}

	return nil, nil // No job available
}

func (q *RedisQueue) GetStatus() int {
	highCount := q.client.LLen(q.ctx, "queue:high").Val()
	lowCount := q.client.LLen(q.ctx, "queue:low").Val()
	return int(highCount + lowCount)
}

func (q *RedisQueue) Close() error {
	return q.client.Close()
}
