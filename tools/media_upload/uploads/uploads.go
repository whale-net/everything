// Package uploads validates media upload requests and mints presigned PUT
// URLs plus the matching public URL. It holds no S3 client itself.
package uploads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// allowedTypes maps a lowercase file extension to its canonical MIME type.
var allowedTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".webp": "image/webp", ".svg": "image/svg+xml", ".gif": "image/gif",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg",
	".flac": "audio/flac", ".m4a": "audio/mp4",
}

// AllowedExtensions returns the extension -> MIME allowlist (a copy).
func AllowedExtensions() map[string]string {
	out := make(map[string]string, len(allowedTypes))
	for k, v := range allowedTypes {
		out[k] = v
	}
	return out
}

// PutOptions are the headers a presigned PUT pins.
type PutOptions struct {
	ContentType   string
	ContentLength int64
	CacheControl  string
	ACL           string
}

// Presigner signs a PUT for key.
type Presigner interface {
	PresignPut(ctx context.Context, key string, opts PutOptions, ttl time.Duration) (string, error)
}

// Config configures a Service.
type Config struct {
	MaxBytes        int64
	PublicBaseURL   string
	PresignTTL      time.Duration
	TempPrefix      string // keys under it are expected to expire via bucket lifecycle
	PermanentPrefix string
	Now             func() time.Time
	NewID           func() string
}

// Service issues presigned uploads.
type Service struct {
	cfg Config
	p   Presigner
}

// New validates cfg and returns a Service.
func New(cfg Config, p Presigner) (*Service, error) {
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("uploads: MaxBytes must be positive")
	}
	if cfg.PublicBaseURL == "" {
		return nil, errors.New("uploads: PublicBaseURL is required")
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	if cfg.TempPrefix == "" {
		cfg.TempPrefix = "tmp"
	}
	if cfg.PermanentPrefix == "" {
		cfg.PermanentPrefix = "permanent"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	return &Service{cfg: cfg, p: p}, nil
}

// MaxBytes is the per-upload size cap.
func (s *Service) MaxBytes() int64 { return s.cfg.MaxBytes }

// Request describes one upload.
type Request struct {
	Filename    string
	ContentType string // optional; must match the extension's type if set
	SizeBytes   int64
	Temporary   bool // false = permanent prefix
}

// Result is what the caller needs to upload and embed.
type Result struct {
	UploadURL string            `json:"upload_url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	PublicURL string            `json:"public_url"`
	Key       string            `json:"key"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// ErrInvalid marks a request the caller can fix.
var ErrInvalid = errors.New("invalid upload request")

// Presign validates r and returns a signed PUT.
func (s *Service) Presign(ctx context.Context, r Request) (*Result, error) {
	name := sanitizeName(r.Filename)
	ext := strings.ToLower(path.Ext(name))
	want, ok := allowedTypes[ext]
	if !ok {
		return nil, fmt.Errorf("%w: extension %q is not allowed", ErrInvalid, ext)
	}
	if r.ContentType != "" && !strings.EqualFold(r.ContentType, want) {
		return nil, fmt.Errorf("%w: content_type %q does not match %s (%s)", ErrInvalid, r.ContentType, ext, want)
	}
	if r.SizeBytes <= 0 || r.SizeBytes > s.cfg.MaxBytes {
		return nil, fmt.Errorf("%w: size_bytes must be in 1..%d", ErrInvalid, s.cfg.MaxBytes)
	}

	prefix := s.cfg.PermanentPrefix
	if r.Temporary {
		prefix = s.cfg.TempPrefix
	}
	now := s.cfg.Now().UTC()
	// A random id per upload means re-uploads never collide in CDN or GitHub caches.
	key := fmt.Sprintf("%s/%s/%s/%s", prefix, now.Format("2006/01"), s.cfg.NewID(), name)
	opts := PutOptions{ContentType: want, ContentLength: r.SizeBytes, CacheControl: "public, max-age=31536000, immutable", ACL: "public-read"}

	url, err := s.p.PresignPut(ctx, key, opts, s.cfg.PresignTTL)
	if err != nil {
		return nil, fmt.Errorf("presign: %w", err)
	}
	return &Result{
		UploadURL: url,
		Method:    "PUT",
		Headers:   map[string]string{"Content-Type": opts.ContentType, "Cache-Control": opts.CacheControl, "x-amz-acl": opts.ACL},
		PublicURL: s.cfg.PublicBaseURL + "/" + key,
		Key:       key,
		ExpiresAt: now.Add(s.cfg.PresignTTL),
	}, nil
}

// sanitizeName keeps the base name and reduces it to URL-safe characters.
func sanitizeName(n string) string {
	n = path.Base(strings.ReplaceAll(n, "\\", "/"))
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if len(out) > 100 {
		ext := path.Ext(out)
		out = out[:100-len(ext)] + ext
	}
	return out
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
