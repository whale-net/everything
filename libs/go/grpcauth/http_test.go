package grpcauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubVerifier struct{ err error }

func (s stubVerifier) Verify(context.Context, string) (*Claims, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &Claims{Subject: "u"}, nil
}

func TestHTTPMiddleware(t *testing.T) {
	cases := []struct {
		name, header string
		err          error
		want         int
	}{
		{"missing", "", nil, 401},
		{"invalid", "Bearer x", errors.New("bad"), 401},
		{"valid", "Bearer x", nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := HTTPMiddleware(stubVerifier{tc.err})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c, ok := ClaimsFromContext(r.Context()); !ok || c.Subject != "u" {
					t.Error("claims not attached")
				}
			}))
			req := httptest.NewRequest("GET", "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d want %d", rec.Code, tc.want)
			}
		})
	}
}
