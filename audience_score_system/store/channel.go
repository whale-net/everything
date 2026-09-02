package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelStore covers `channel` (migration 001).
type ChannelStore interface {
	// Create inserts a channel row plus its role=creator channel_person
	// row for creatorPersonID, atomically (one transaction) -- the only
	// way a Channel ever gets a creator (FR3, LB2).
	Create(ctx context.Context, youtubeChannelID, title string, creatorPersonID uuid.UUID) (Channel, error)

	// GetByID returns the Channel for id, or an error if none exists.
	GetByID(ctx context.Context, id uuid.UUID) (Channel, error)

	// GetByYouTubeChannelID returns the Channel for youtubeChannelID
	// (`channel.youtube_channel_id`, UNIQUE), or pgx.ErrNoRows if no
	// Channel has ever connected that YouTube channel -- the
	// Channel-connect callback's (#1571) "does this Channel already
	// exist" check.
	GetByYouTubeChannelID(ctx context.Context, youtubeChannelID string) (Channel, error)

	// SetConnectionState updates connection_state and stamps
	// connection_state_changed_at (FR4).
	SetConnectionState(ctx context.Context, channelID uuid.UUID, state ConnectionState) error

	// ListConnected returns every Channel with ConnectionState ==
	// ConnectionStateConnected, e.g. for the worker's per-Channel sync
	// schedule.
	ListConnected(ctx context.Context) ([]Channel, error)
}

// channelStore implements ChannelStore against `channel` and
// `channel_person` (migration 001).
type channelStore struct{ pool *pgxpool.Pool }

var _ ChannelStore = channelStore{}

const channelColumns = `id, youtube_channel_id, COALESCE(title, ''), connection_state, connection_state_changed_at, created_at`

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	err := row.Scan(&c.ID, &c.YouTubeChannelID, &c.Title, &c.ConnectionState, &c.ConnectionStateChangedAt, &c.CreatedAt)
	return c, err
}

// Create inserts the channel with an initial connection_state of
// "connected" -- a Channel is only ever created after its creator has
// just completed the YouTube OAuth connection (FR3) -- plus a
// role=creator channel_person row for creatorPersonID, in one
// transaction so a Channel can never exist without a creator.
func (s channelStore) Create(ctx context.Context, youtubeChannelID, title string, creatorPersonID uuid.UUID) (Channel, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Channel{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	ch, err := scanChannel(tx.QueryRow(ctx, `
		INSERT INTO channel (youtube_channel_id, title, connection_state)
		VALUES ($1, $2, $3)
		RETURNING `+channelColumns,
		youtubeChannelID, title, ConnectionStateConnected))
	if err != nil {
		return Channel{}, fmt.Errorf("insert channel: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO channel_person (channel_id, person_id, role) VALUES ($1, $2, $3)
	`, ch.ID, creatorPersonID, RoleCreator); err != nil {
		return Channel{}, fmt.Errorf("insert creator channel_person row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Channel{}, fmt.Errorf("commit: %w", err)
	}
	return ch, nil
}

func (s channelStore) GetByID(ctx context.Context, id uuid.UUID) (Channel, error) {
	ch, err := scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelColumns+` FROM channel WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, pgx.ErrNoRows
		}
		return Channel{}, fmt.Errorf("get channel by id: %w", err)
	}
	return ch, nil
}

// SetConnectionState errors if channelID does not exist, rather than
// silently no-op'ing on a zero-row UPDATE (FR4).
func (s channelStore) GetByYouTubeChannelID(ctx context.Context, youtubeChannelID string) (Channel, error) {
	ch, err := scanChannel(s.pool.QueryRow(ctx, `SELECT `+channelColumns+` FROM channel WHERE youtube_channel_id = $1`, youtubeChannelID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, pgx.ErrNoRows
		}
		return Channel{}, fmt.Errorf("get channel by youtube channel id: %w", err)
	}
	return ch, nil
}

func (s channelStore) SetConnectionState(ctx context.Context, channelID uuid.UUID, state ConnectionState) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE channel
		SET connection_state = $1, connection_state_changed_at = NOW()
		WHERE id = $2
	`, state, channelID)
	if err != nil {
		return fmt.Errorf("set connection state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("channel %s: %w", channelID, pgx.ErrNoRows)
	}
	return nil
}

func (s channelStore) ListConnected(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+channelColumns+`
		FROM channel
		WHERE connection_state = $1
		ORDER BY created_at
	`, ConnectionStateConnected)
	if err != nil {
		return nil, fmt.Errorf("list connected channels: %w", err)
	}
	defer rows.Close()

	var channels []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list connected channels: %w", err)
	}
	return channels, nil
}
