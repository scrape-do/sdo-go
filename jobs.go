// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// jobsAPIPrefix is the versioned REST base path on q.scrape.do.
const jobsAPIPrefix = "/api/v1"

// jobIDRe is what a job or task id must look like before it enters the URL
// path. Ids are UUIDs; a caller passing one through from its own users must not
// be able to reach another endpoint with "../me" or inject "?x=".
var jobIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func checkID(what, id string) error {
	if !jobIDRe.MatchString(id) {
		return &ConfigError{Message: fmt.Sprintf(
			"invalid %s: it must be 1-128 characters of A-Z, a-z, 0-9, '_' or '-'", what)}
	}
	return nil
}

// The job API's responses are decoded leniently (every SDK does the same): a
// field that is absent or of the wrong type reads as its zero value, and in a
// string map an entry whose value is not a string is dropped. Keys match
// exactly. One odd field therefore never fails a whole call; a body that is
// not a JSON object does (a *TransportError).

// Job is the acknowledgement returned by Create.
type Job struct {
	JobID   string   `json:"JobID"`
	Message string   `json:"Message"`
	TaskIDs []string `json:"TaskIDs"`
}

// UnmarshalJSON decodes a job acknowledgement leniently.
func (j *Job) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) {
		*j = Job{JobID: f.str("JobID"), Message: f.str("Message"), TaskIDs: f.strList("TaskIDs")}
	})
}

// Task is one target within a job.
type Task struct {
	TaskID string `json:"TaskID"`
	URL    string `json:"URL"`
	Status string `json:"Status"`
}

// UnmarshalJSON decodes a task leniently.
func (t *Task) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) { *t = taskFrom(f) })
}

func taskFrom(f jobFields) Task {
	return Task{TaskID: f.str("TaskID"), URL: f.str("URL"), Status: f.str("Status")}
}

// JobStatus is the full state of a job and its tasks.
type JobStatus struct {
	JobID     string   `json:"JobID"`
	TaskIDs   []string `json:"TaskIDs"`
	Status    string   `json:"Status"`
	StartTime string   `json:"StartTime"`
	EndTime   string   `json:"EndTime"`
	Canceled  bool     `json:"Canceled"`
	Tasks     []Task   `json:"Tasks"`
}

// UnmarshalJSON decodes a job status leniently.
func (s *JobStatus) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) { *s = jobStatusFrom(f) })
}

func jobStatusFrom(f jobFields) JobStatus {
	st := JobStatus{
		JobID: f.str("JobID"), TaskIDs: f.strList("TaskIDs"), Status: f.str("Status"),
		StartTime: f.str("StartTime"), EndTime: f.str("EndTime"), Canceled: f.boolean("Canceled"),
	}
	for _, t := range f.objects("Tasks") {
		st.Tasks = append(st.Tasks, taskFrom(t))
	}
	return st
}

// TaskResult is the delivered content of a single task. Absent or mistyped
// fields are zero. Times are the API's RFC 3339 strings.
type TaskResult struct {
	TaskID string `json:"TaskID"`
	JobID  string `json:"JobID"`
	URL    string `json:"URL"`
	// Status is one of queuing, queued, pending, rotating, success, error,
	// canceled.
	Status     string `json:"Status"`
	StartTime  string `json:"StartTime"`
	EndTime    string `json:"EndTime"`
	ExpiresAt  string `json:"ExpiresAt"`
	UpdateTime string `json:"UpdateTime"`
	// Base64EncodedContent reports that Content is base64 (a binary target);
	// ContentBytes decodes it.
	Base64EncodedContent bool              `json:"Base64EncodedContent"`
	StatusCode           int               `json:"StatusCode"`
	ResponseHeaders      map[string]string `json:"ResponseHeaders"`
	// ScrapeDo is the gateway's metadata map (the "Scrape.do" key).
	ScrapeDo     map[string]string `json:"Scrape.do"`
	Content      string            `json:"Content"`
	ErrorMessage string            `json:"ErrorMessage"`
}

// UnmarshalJSON decodes a task result leniently.
func (t *TaskResult) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) {
		code, _ := f.integer("StatusCode")
		*t = TaskResult{
			TaskID: f.str("TaskID"), JobID: f.str("JobID"), URL: f.str("URL"), Status: f.str("Status"),
			StartTime: f.str("StartTime"), EndTime: f.str("EndTime"), ExpiresAt: f.str("ExpiresAt"),
			UpdateTime: f.str("UpdateTime"), Base64EncodedContent: f.boolean("Base64EncodedContent"),
			StatusCode: code, ResponseHeaders: f.strMap("ResponseHeaders"), ScrapeDo: f.strMap("Scrape.do"),
			Content: f.str("Content"), ErrorMessage: f.str("ErrorMessage"),
		}
	})
}

// ContentBytes returns the task's content as bytes: base64-decoded when
// Base64EncodedContent is set, otherwise the UTF-8 bytes of Content. Invalid
// base64 is a *TransportError.
func (t *TaskResult) ContentBytes() ([]byte, error) {
	if !t.Base64EncodedContent {
		return []byte(t.Content), nil
	}
	b, err := base64.StdEncoding.DecodeString(t.Content)
	if err != nil {
		return nil, &TransportError{Message: "task content is not valid base64", Err: err}
	}
	return b, nil
}

// Account is the caller's concurrency and credit snapshot.
type Account struct {
	TotalConcurrency int `json:"TotalConcurrency"`
	FreeConcurrency  int `json:"FreeConcurrency"`
	ActiveJobs       int `json:"ActiveJobs"`
	// AvailableCredits reads "AvailableCredits", falling back to the API's
	// misspelled "AvaliableCredits".
	AvailableCredits int `json:"AvailableCredits"`
}

// UnmarshalJSON decodes an account snapshot leniently.
func (a *Account) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) {
		total, _ := f.integer("TotalConcurrency")
		free, _ := f.integer("FreeConcurrency")
		active, _ := f.integer("ActiveJobs")
		credits, ok := f.integer("AvailableCredits")
		if !ok {
			credits, _ = f.integer("AvaliableCredits")
		}
		*a = Account{TotalConcurrency: total, FreeConcurrency: free, ActiveJobs: active, AvailableCredits: credits}
	})
}

// AsyncJobs wraps the q.scrape.do REST job surface. Every request authenticates
// via the X-Token header (never a token= query) and goes through the owning
// client's transport: its timeout, size cap, retry policy and redaction. Any
// non-2xx response, a redirect included, is an *APIError; a success body that
// is not valid JSON is a *TransportError.
type AsyncJobs struct {
	t       *transport // holds the token; see Client.t
	baseURL string
}

// String describes the job client without its token.
func (j AsyncJobs) String() string {
	return fmt.Sprintf("sdo.AsyncJobs{BaseURL: %q, Token: %q}", j.baseURL, redactMask)
}

// GoString is String, for %#v.
func (j AsyncJobs) GoString() string { return j.String() }

// do issues one REST request and, on success, decodes the JSON body into out
// (which may be nil to discard it).
func (j *AsyncJobs) do(ctx context.Context, method, path string, body []byte, out any) error {
	header := http.Header{}
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	in, err := j.t.do(ctx, outgoing{
		method: method,
		url:    j.baseURL + jobsAPIPrefix + path,
		header: header,
		body:   body,
		jobAPI: true,
	})
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	err = unmarshalBody(in.body, out)
	switch {
	case errors.Is(err, errNotObject) || (err == nil && !isJSONObjectText(in.body)):
		return &TransportError{Message: "job API response is not a JSON object", Err: j.t.cause(errNotObject)}
	case err != nil:
		return &TransportError{Message: "job API response is not valid JSON", Err: j.t.cause(err)}
	}
	return nil
}

// Create submits a job for one or more target URLs. options is merged into the
// JSON body using the API's own PascalCase job-body keys (e.g. "Super",
// "GeoCode", "Render", "Output"); it is not ScrapeConfig query serialization.
// The body is canonical JSON. targets must be non-empty, and options may not
// set "Targets".
func (j *AsyncJobs) Create(ctx context.Context, targets []string, options map[string]any) (*Job, error) {
	if len(targets) == 0 {
		return nil, &ConfigError{Message: "Create needs at least one target URL"}
	}
	payload := make(map[string]any, len(options)+1)
	for k, v := range options {
		if strings.EqualFold(k, "Targets") {
			return nil, &ConfigError{Message: "options may not set Targets; pass them as the targets argument"}
		}
		payload[k] = v
	}
	payload["Targets"] = targets
	body, err := canonicalJSON(payload)
	if err != nil {
		return nil, &ConfigError{Message: fmt.Sprintf("job options are not JSON-serializable: %v", err)}
	}
	var job Job
	if err := j.do(ctx, http.MethodPost, "/jobs", []byte(body), &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// Status returns the current state of a job and its tasks.
func (j *AsyncJobs) Status(ctx context.Context, jobID string) (*JobStatus, error) {
	if err := checkID("job ID", jobID); err != nil {
		return nil, err
	}
	var st JobStatus
	if err := j.do(ctx, http.MethodGet, "/jobs/"+url.PathEscape(jobID), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// Result returns the delivered content of a task within a job.
func (j *AsyncJobs) Result(ctx context.Context, jobID, taskID string) (*TaskResult, error) {
	if err := checkID("job ID", jobID); err != nil {
		return nil, err
	}
	if err := checkID("task ID", taskID); err != nil {
		return nil, err
	}
	var tr TaskResult
	path := "/jobs/" + url.PathEscape(jobID) + "/" + url.PathEscape(taskID)
	if err := j.do(ctx, http.MethodGet, path, nil, &tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// jobList is the List response, {"Jobs": [...]}.
type jobList struct{ Jobs []JobStatus }

func (l *jobList) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) {
		for _, s := range f.objects("Jobs") {
			l.Jobs = append(l.Jobs, jobStatusFrom(s))
		}
	})
}

// List returns every job visible to the caller.
func (j *AsyncJobs) List(ctx context.Context) ([]JobStatus, error) {
	var out jobList
	if err := j.do(ctx, http.MethodGet, "/jobs", nil, &out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

// Cancel cancels a job by id. Any 2xx is success; the body is ignored.
func (j *AsyncJobs) Cancel(ctx context.Context, jobID string) error {
	if err := checkID("job ID", jobID); err != nil {
		return err
	}
	return j.do(ctx, http.MethodDelete, "/jobs/"+url.PathEscape(jobID), nil, nil)
}

// Me returns the caller's concurrency and credit snapshot.
func (j *AsyncJobs) Me(ctx context.Context) (*Account, error) {
	var acc Account
	if err := j.do(ctx, http.MethodGet, "/me", nil, &acc); err != nil {
		return nil, err
	}
	return &acc, nil
}

// errNotObject is a job API body that is valid JSON but not an object.
var errNotObject = errors.New("not a JSON object")

// decodeLenient decodes a JSON object into fields and hands them to fill. A
// JSON null leaves the value unchanged (the json.Unmarshaler convention).
func decodeLenient(data []byte, fill func(jobFields)) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return errNotObject
	}
	fill(jobFields(m))
	return nil
}

// isJSONObjectText reports whether body (after a BOM and whitespace) starts an
// object; it tells a null body apart after a successful decode.
func isJSONObjectText(body []byte) bool {
	body = bytes.TrimLeft(bytes.TrimPrefix(body, bomUTF8), " \t\r\n")
	return len(body) > 0 && body[0] == '{'
}

// jobFields is one decoded job API object; each accessor treats a missing or
// wrongly typed value as absent.
type jobFields map[string]any

func (f jobFields) str(k string) string {
	s, _ := f[k].(string)
	return s
}

func (f jobFields) boolean(k string) bool {
	b, _ := f[k].(bool)
	return b
}

// integer reads a JSON number with an integral value that fits an int.
func (f jobFields) integer(k string) (int, bool) {
	n, ok := f[k].(json.Number)
	if !ok {
		return 0, false
	}
	if i, err := strconv.ParseInt(string(n), 10, 0); err == nil {
		return int(i), true
	}
	fl, err := strconv.ParseFloat(string(n), 64)
	if err != nil || fl != math.Trunc(fl) || math.Abs(fl) > 1<<53 {
		return 0, false
	}
	return int(fl), true
}

// strMap keeps the string-valued entries of an object.
func (f jobFields) strMap(k string) map[string]string {
	m, ok := f[k].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for key, v := range m {
		if s, ok := v.(string); ok {
			out[key] = s
		}
	}
	return out
}

// strList keeps the string elements of an array.
func (f jobFields) strList(k string) []string {
	l, ok := f[k].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(l))
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// objects keeps the object elements of an array.
func (f jobFields) objects(k string) []jobFields {
	l, _ := f[k].([]any)
	var out []jobFields
	for _, v := range l {
		if m, ok := v.(map[string]any); ok {
			out = append(out, jobFields(m))
		}
	}
	return out
}
