package auth

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const sealPurposeClient = "client"

type sealedClient struct {
	Meta      oauthex.ClientRegistrationMetadata `json:"m"`
	CreatedAt time.Time                          `json:"t"`
}

type sealedClientRegistry struct{ sealer *Sealer }

// NewSealedClientRegistry is a ClientRegistry that stores nothing: the
// client_id it issues is the sealed registration metadata itself, so any
// replica holding the same Sealer secret recognises it.
func NewSealedClientRegistry(sealer *Sealer) ClientRegistry {
	return &sealedClientRegistry{sealer: sealer}
}

func (r *sealedClientRegistry) Register(_ context.Context, meta oauthex.ClientRegistrationMetadata) (OAuthClient, error) {
	now := time.Now().UTC()
	id, err := r.sealer.Seal(sealPurposeClient, sealedClient{Meta: meta, CreatedAt: now})
	if err != nil {
		return OAuthClient{}, err
	}
	return OAuthClient{ClientID: id, RedirectURIs: meta.RedirectURIs, Metadata: meta, CreatedAt: now}, nil
}

func (r *sealedClientRegistry) Get(_ context.Context, clientID string) (OAuthClient, error) {
	var c sealedClient
	if err := r.sealer.Open(sealPurposeClient, clientID, &c); err != nil {
		return OAuthClient{}, ErrClientNotFound
	}
	return OAuthClient{ClientID: clientID, RedirectURIs: c.Meta.RedirectURIs, Metadata: c.Meta, CreatedAt: c.CreatedAt}, nil
}
