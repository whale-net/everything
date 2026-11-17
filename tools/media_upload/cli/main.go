// Command media-upload uploads a local file via the media-upload MCP server
// and prints its public URL.
//
// Auth (first match): MEDIA_UPLOAD_TOKEN (bearer token), or OAuth client
// credentials via MEDIA_UPLOAD_TOKEN_URL + MEDIA_UPLOAD_CLIENT_ID +
// MEDIA_UPLOAD_CLIENT_SECRET.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/oauth2/clientcredentials"

	"github.com/whale-net/everything/tools/media_upload/client"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("media-upload", flag.ContinueOnError)
	url := fs.String("url", os.Getenv("MEDIA_UPLOAD_MCP_URL"), "media-upload MCP endpoint (env MEDIA_UPLOAD_MCP_URL)")
	temporary := fs.Bool("temporary", false, "store under the expiring prefix")
	markdown := fs.Bool("markdown", false, "print a Markdown image/link instead of the bare URL")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: media-upload [flags] <file>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("exactly one file is required")
	}
	if *url == "" {
		return fmt.Errorf("--url or MEDIA_UPLOAD_MCP_URL is required")
	}

	ctx := context.Background()
	httpc, err := authClient(ctx)
	if err != nil {
		return err
	}
	path := fs.Arg(0)
	res, err := client.Upload(ctx, client.Options{MCPURL: *url, HTTP: httpc, Path: path, Temporary: *temporary})
	if err != nil {
		return err
	}
	if *markdown {
		fmt.Printf("![%s](%s)\n", filepath.Base(path), res.PublicURL)
	} else {
		fmt.Println(res.PublicURL)
	}
	return nil
}

func authClient(ctx context.Context) (*http.Client, error) {
	if tok := os.Getenv("MEDIA_UPLOAD_TOKEN"); tok != "" {
		return client.WithBearer(tok), nil
	}
	tokenURL, id, secret := os.Getenv("MEDIA_UPLOAD_TOKEN_URL"), os.Getenv("MEDIA_UPLOAD_CLIENT_ID"), os.Getenv("MEDIA_UPLOAD_CLIENT_SECRET")
	if tokenURL == "" || id == "" || secret == "" {
		return nil, fmt.Errorf("set MEDIA_UPLOAD_TOKEN, or MEDIA_UPLOAD_TOKEN_URL + MEDIA_UPLOAD_CLIENT_ID + MEDIA_UPLOAD_CLIENT_SECRET")
	}
	cc := clientcredentials.Config{ClientID: id, ClientSecret: secret, TokenURL: tokenURL}
	return cc.Client(ctx), nil
}
