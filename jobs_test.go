// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// newJobsClient wires a Client whose async base URL points at a mock server.
func newJobsClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New("t", WithAsyncBaseURL(srv.URL), WithBackoff(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestJobsCreate(t *testing.T) {
	var (
		method, path, token, contentType string
		body                             map[string]any
	)
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"JobID":"job-1","Message":"queued","TaskIDs":["t1","t2"]}`))
	})

	job, err := c.Jobs().Create(context.Background(), []string{"https://a.com", "https://b.com"},
		map[string]any{"Super": true, "GeoCode": "us"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if method != http.MethodPost || path != "/api/v1/jobs" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q, want t", token)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	targets, _ := body["Targets"].([]any)
	if len(targets) != 2 || targets[0] != "https://a.com" || targets[1] != "https://b.com" {
		t.Errorf("Targets = %v", body["Targets"])
	}
	if body["Super"] != true || body["GeoCode"] != "us" {
		t.Errorf("options not merged into body: %v", body)
	}
	if job.JobID != "job-1" || job.Message != "queued" || !reflect.DeepEqual(job.TaskIDs, []string{"t1", "t2"}) {
		t.Errorf("parsed job = %+v", job)
	}
}

func TestJobsStatus(t *testing.T) {
	var method, path, token string
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"JobID":"job-1","TaskIDs":["t1"],"Status":"running","StartTime":"2026-07-16T00:00:00Z","EndTime":"","Canceled":false,"Tasks":[{"TaskID":"t1","URL":"https://a.com","Status":"done"}]}`))
	})

	st, err := c.Jobs().Status(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if method != http.MethodGet || path != "/api/v1/jobs/job-1" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q", token)
	}
	if st.JobID != "job-1" || st.Status != "running" || st.Canceled {
		t.Errorf("parsed status = %+v", st)
	}
	if len(st.Tasks) != 1 || st.Tasks[0].TaskID != "t1" || st.Tasks[0].URL != "https://a.com" || st.Tasks[0].Status != "done" {
		t.Errorf("parsed tasks = %+v", st.Tasks)
	}
}

func TestJobsResult(t *testing.T) {
	var method, path, token string
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"TaskID":"t1","JobID":"job-1","URL":"https://a.com","Status":"done","StatusCode":200,"Base64EncodedContent":true,"ResponseHeaders":{"Content-Type":"text/html"},"Scrape.do":{"Request-Cost":"1"},"Content":"aGk="}`))
	})

	tr, err := c.Jobs().Result(context.Background(), "job-1", "t1")
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if method != http.MethodGet || path != "/api/v1/jobs/job-1/t1" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q", token)
	}
	if tr.TaskID != "t1" || tr.JobID != "job-1" || tr.StatusCode != 200 || tr.Content != "aGk=" ||
		!tr.Base64EncodedContent {
		t.Errorf("parsed result = %+v", tr)
	}
	if tr.ResponseHeaders["Content-Type"] != "text/html" || tr.ScrapeDo["Request-Cost"] != "1" {
		t.Errorf("ResponseHeaders = %v, ScrapeDo = %v", tr.ResponseHeaders, tr.ScrapeDo)
	}
	if b, err := tr.ContentBytes(); err != nil || string(b) != "hi" {
		t.Errorf("ContentBytes = %q, %v", b, err)
	}
}

func TestJobsList(t *testing.T) {
	var method, path, token string
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Jobs":[{"JobID":"job-1","Status":"done"},{"JobID":"job-2","Status":"running"}]}`))
	})

	jobs, err := c.Jobs().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if method != http.MethodGet || path != "/api/v1/jobs" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q", token)
	}
	if len(jobs) != 2 || jobs[0].JobID != "job-1" || jobs[1].JobID != "job-2" {
		t.Errorf("parsed jobs = %+v", jobs)
	}
}

func TestJobsCancel(t *testing.T) {
	var method, path, token string
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		w.WriteHeader(http.StatusOK)
	})

	if err := c.Jobs().Cancel(context.Background(), "job-1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if method != http.MethodDelete || path != "/api/v1/jobs/job-1" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q", token)
	}
}

func TestJobsMe(t *testing.T) {
	var method, path, token string
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		token = r.Header.Get("X-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"TotalConcurrency":10,"FreeConcurrency":4,"ActiveJobs":2,"AvaliableCredits":500}`))
	})

	acc, err := c.Jobs().Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if method != http.MethodGet || path != "/api/v1/me" {
		t.Errorf("request line: %s %s", method, path)
	}
	if token != "t" {
		t.Errorf("X-Token = %q", token)
	}
	// The API misspells the key as "AvaliableCredits"; it must map to AvailableCredits.
	if acc.TotalConcurrency != 10 || acc.FreeConcurrency != 4 || acc.ActiveJobs != 2 || acc.AvailableCredits != 500 {
		t.Errorf("parsed account = %+v", acc)
	}
}

func TestJobsErrorEnvelope(t *testing.T) {
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"Error":"Targets is required","Code":400}`))
	})

	_, err := c.Jobs().Create(context.Background(), []string{"https://a.com"}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if !apiErr.IsValidation() {
		t.Errorf("kind = %v, want validation", apiErr.Kind)
	}
	if apiErr.Message != "Targets is required" {
		t.Errorf("Message = %q, want parsed envelope message", apiErr.Message)
	}
}

// Every job operation surfaces gateway errors as *APIError rather than a
// half-filled value.
func TestJobsOperationsSurfaceAPIErrors(t *testing.T) {
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"Error":"boom","Code":500}`))
	})
	ctx := context.Background()
	calls := map[string]func() error{
		"Status": func() error { _, err := c.Jobs().Status(ctx, "j"); return err },
		"Result": func() error { _, err := c.Jobs().Result(ctx, "j", "t"); return err },
		"List":   func() error { _, err := c.Jobs().List(ctx); return err },
		"Me":     func() error { _, err := c.Jobs().Me(ctx); return err },
	}
	for name, call := range calls {
		var apiErr *APIError
		if err := call(); !errors.As(err, &apiErr) || apiErr.StatusCode != 500 || apiErr.Message != "boom" {
			t.Errorf("%s: want APIError(500, boom), got %v", name, err)
		}
	}
}

func TestJobsMalformedJSONIsAnError(t *testing.T) {
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	})
	if _, err := c.Jobs().Status(context.Background(), "j"); err == nil {
		t.Error("a non-JSON 200 decoded without error")
	}
}

func TestJobsCreateRejectsUnserializableOptions(t *testing.T) {
	c := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("an unserializable body must not reach the wire")
	})
	_, err := c.Jobs().Create(context.Background(), []string{"https://e.com"},
		map[string]any{"Bad": func() {}})
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Errorf("want ConfigError, got %v", err)
	}
}

func TestJobsTransportFailures(t *testing.T) {
	ctx := context.Background()

	var ce *ConfigError
	if _, err := New("t", WithAsyncBaseURL("https://bad host")); !errors.As(err, &ce) {
		t.Errorf("unparseable async URL: want ConfigError, got %v", err)
	}

	down, _ := New("t", WithAsyncBaseURL("http://127.0.0.1:1"), WithRetries(0))
	var te *TransportError
	if _, err := down.Jobs().Me(ctx); !errors.As(err, &te) {
		t.Errorf("unreachable host: want TransportError, got %v", err)
	}

	truncated := newJobsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte(`{"a"`))
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	})
	if _, err := truncated.Jobs().Me(ctx); !errors.As(err, &te) || te.Message != "failed to read response body" {
		t.Errorf("truncated body: want a read TransportError, got %v", err)
	}
}

// Job responses decode leniently, and the error for a body that is valid JSON
// but not an object says so.
func TestJobsLenientDecodingAndBodyShape(t *testing.T) {
	cleanEnv(t)
	bodies := map[string]string{
		"/api/v1/jobs":     `{"Jobs":[{"JobID":"a","Canceled":"yes","Tasks":[{"TaskID":1},{"TaskID":"t"},7]},"junk"]}`,
		"/api/v1/jobs/j-1": `{"JobID":"j-1","TaskIDs":["t1",2,"t2"],"Canceled":true,"Status":9}`,
		"/api/v1/me":       `{"TotalConcurrency":"5","FreeConcurrency":4.0,"ActiveJobs":1.5,"AvailableCredits":"x","AvaliableCredits":7}`,
	}
	jobs, _ := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			io.WriteString(w, `{"JobID":"new","TaskIDs":"t1","Message":null}`)
			return
		}
		io.WriteString(w, bodies[r.URL.Path])
	})
	ctx := context.Background()
	list, err := jobs.List(ctx)
	if err != nil || len(list) != 1 || list[0].JobID != "a" || list[0].Canceled || len(list[0].Tasks) != 2 || list[0].Tasks[1].TaskID != "t" {
		t.Errorf("List = %+v, %v", list, err)
	}
	st, err := jobs.Status(ctx, "j-1")
	if err != nil || fmt.Sprint(st.TaskIDs) != "[t1 t2]" || !st.Canceled || st.Status != "" {
		t.Errorf("Status = %+v, %v", st, err)
	}
	acc, err := jobs.Me(ctx)
	if err != nil || *acc != (Account{FreeConcurrency: 4, AvailableCredits: 7}) {
		t.Errorf("Me = %+v, %v", acc, err)
	}
	job, err := jobs.Create(ctx, []string{"https://e.com"}, nil)
	if err != nil || job.JobID != "new" || job.TaskIDs != nil || job.Message != "" {
		t.Errorf("Create = %+v, %v", job, err)
	}
	var task Task
	if err := json.Unmarshal([]byte(`{"TaskID":"t","URL":7,"Status":"done"}`), &task); err != nil || task != (Task{TaskID: "t", Status: "done"}) {
		t.Errorf("Task = %+v, %v", task, err)
	}
	var tr TaskResult
	if err := json.Unmarshal([]byte(`null`), &tr); err != nil || tr.TaskID != "" {
		t.Errorf("null leaves the value alone: %v", err)
	}
	if err := json.Unmarshal([]byte(`[1]`), &tr); !errors.Is(err, errNotObject) {
		t.Errorf("an array is not a task result: %v", err)
	}
	if err := tr.UnmarshalJSON([]byte(`{"TaskID":`)); err == nil || tr.TaskID != "" {
		t.Errorf("a direct call with broken JSON must fail and leave the value alone: %v", err)
	}

	for body, want := range map[string]string{
		`[]`:          "job API response is not a JSON object",
		`null`:        "job API response is not a JSON object",
		`"s"`:         "job API response is not a JSON object",
		``:            "job API response is not valid JSON",
		`{"a":NaN`:    "job API response is not valid JSON",
		`{"a":1e999}`: "job API response is not valid JSON",
	} {
		jobs, _ := newCorpusJobs(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		_, err := jobs.Status(ctx, "j-1")
		var te *TransportError
		if !errors.As(err, &te) || te.Message != want {
			t.Errorf("body %q: got %v, want %q", body, err, want)
		}
	}
}
