// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestErrorTypes(t *testing.T) {
	ce := &ConfigError{Message: "bad config"}
	if ce.Error() != "bad config" {
		t.Errorf("ConfigError.Error = %q", ce.Error())
	}

	inner := errors.New("dial fail")
	te := &TransportError{Message: "connection failed", Err: inner}
	if te.Error() != "connection failed" || !errors.Is(te, inner) {
		t.Errorf("TransportError unwrap failed")
	}

	ae := &APIError{StatusCode: 401, Kind: KindAuth, Message: "nope"}
	if ae.Error() == "" || !ae.IsAuth() {
		t.Errorf("APIError: %q auth=%v", ae.Error(), ae.IsAuth())
	}
	other := &APIError{StatusCode: 500, Kind: KindGateway, Message: "boom"}
	if other.Error() == "" {
		t.Error("APIError.Error empty")
	}

	preds := []struct {
		e  *APIError
		ok bool
	}{
		{&APIError{Kind: KindValidation}, (&APIError{Kind: KindValidation}).IsValidation()},
		{&APIError{Kind: KindConcurrency}, (&APIError{Kind: KindConcurrency}).IsConcurrency()},
		{&APIError{Kind: KindGateway}, (&APIError{Kind: KindGateway}).IsGateway()},
	}
	for _, p := range preds {
		if !p.ok {
			t.Errorf("predicate failed for kind %v", p.e.Kind)
		}
	}
}

func TestErrorKindString(t *testing.T) {
	want := map[ErrorKind]string{
		KindOther:       "other",
		KindAuth:        "auth",
		KindValidation:  "validation",
		KindConcurrency: "concurrency",
		KindGateway:     "gateway",
	}
	for k, s := range want {
		if k.String() != s {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", k, k.String(), s)
		}
	}
}

func TestOptionsApply(t *testing.T) {
	c, err := New(
		"t",
		WithTimeout(5*time.Second),
		WithRetries(3),
		WithBackoff(time.Millisecond),
		WithAcceptEncoding("gzip"),
		WithHTTPClient(&http.Client{}),
		WithBaseURL("https://api.example/"),
		WithAsyncBaseURL("https://q.example/"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "https://api.example" || c.asyncBaseURL != "https://q.example" {
		t.Errorf("base urls: %q %q", c.baseURL, c.asyncBaseURL)
	}
}
