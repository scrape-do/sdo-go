// SPDX-License-Identifier: Apache-2.0

package html_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/scrape-do/sdo-go"
	"github.com/scrape-do/sdo-go/html"
)

func TestDocument(t *testing.T) {
	for _, k := range []string{"SDO_TOKEN", "SDO_BASE_URL", "SDO_ASYNC_BASE_URL", "SDO_TIMEOUT"} {
		t.Setenv(k, "") // hermetic: the caller's environment must not change the result
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1252")
		w.Write([]byte("<html><body><h1>Caf\xe9</h1></body></html>"))
	}))
	defer srv.Close()
	c, err := sdo.New("TOKEN-for-tests", sdo.WithBaseURL(srv.URL), sdo.WithRetries(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := c.Scrape(context.Background(), &sdo.ScrapeConfig{URL: "https://e.com"})
	if err != nil {
		t.Fatalf("Scrape: %v", err)
	}
	doc, err := html.Document(res)
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if got := doc.Find("h1").Text(); got != "Café" {
		t.Errorf("h1 = %q, want the charset-decoded text", got)
	}
	var ce *sdo.ConfigError
	if _, err := html.Document(nil); !errors.As(err, &ce) {
		t.Errorf("Document(nil): want a ConfigError, got %v", err)
	}
}
