package uploads

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakePresigner struct {
	key  string
	opts PutOptions
}

func (f *fakePresigner) PresignPut(_ context.Context, key string, o PutOptions, _ time.Duration) (string, error) {
	f.key, f.opts = key, o
	return "https://s3.example/" + key + "?sig=x", nil
}

func newSvc(t *testing.T) (*Service, *fakePresigner) {
	t.Helper()
	f := &fakePresigner{}
	s, err := New(Config{
		MaxBytes: 10 << 20, PublicBaseURL: "https://media.example/",
		Now:   func() time.Time { return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) },
		NewID: func() string { return "abc123" },
	}, f)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestPresignPermanent(t *testing.T) {
	s, f := newSvc(t)
	r, err := s.Presign(context.Background(), Request{Filename: "My Demo.GIF", SizeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if want := "permanent/2026/11/abc123/My_Demo.GIF"; r.Key != want {
		t.Fatalf("key = %q, want %q", r.Key, want)
	}
	if r.PublicURL != "https://media.example/"+r.Key {
		t.Fatalf("public url = %q", r.PublicURL)
	}
	if f.opts.ContentType != "image/gif" || f.opts.ContentLength != 1024 {
		t.Fatalf("opts = %+v", f.opts)
	}
}

func TestPresignTemporaryPrefix(t *testing.T) {
	s, _ := newSvc(t)
	r, err := s.Presign(context.Background(), Request{Filename: "a.mp4", SizeBytes: 5, Temporary: true})
	if err != nil || !strings.HasPrefix(r.Key, "tmp/") {
		t.Fatalf("key=%v err=%v", r, err)
	}
}

func TestPresignRejects(t *testing.T) {
	s, _ := newSvc(t)
	cases := map[string]Request{
		"bad ext":       {Filename: "x.exe", SizeBytes: 1},
		"too big":       {Filename: "x.png", SizeBytes: 11 << 20},
		"zero":          {Filename: "x.png"},
		"type mismatch": {Filename: "x.png", ContentType: "video/mp4", SizeBytes: 1},
	}
	for name, r := range cases {
		if _, err := s.Presign(context.Background(), r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd.png": "passwd.png",
		`C:\x\y z.png`:         "y_z.png",
		".hidden.png":          "hidden.png",
	} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
