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
	"slices"
	"strconv"
	"strings"
	"time"
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

// JobStatus is the full state of a job and its tasks. Status is one of the
// JobStatus* values. AcquiredConcurrency and LimitConcurrency are the job's
// share of the account's separate async concurrency pool.
type JobStatus struct {
	JobID               string   `json:"JobID"`
	TaskIDs             []string `json:"TaskIDs"`
	Status              string   `json:"Status"`
	StartTime           string   `json:"StartTime"`
	EndTime             string   `json:"EndTime"`
	AcquiredConcurrency int      `json:"AcquiredConcurrency"`
	LimitConcurrency    int      `json:"LimitConcurrency"`
	Canceled            bool     `json:"Canceled"`
	Tasks               []Task   `json:"Tasks"`
}

// The job statuses the API documents, for JobStatus.Status, Task.Status,
// TaskResult.Status and the ListJobsOptions.Status filter.
const (
	JobStatusQueuing  = "queuing"
	JobStatusQueued   = "queued"
	JobStatusPending  = "pending"
	JobStatusRotating = "rotating"
	JobStatusSuccess  = "success"
	JobStatusError    = "error"
	JobStatusCanceled = "canceled"
)

var jobStatuses = []string{
	JobStatusQueuing, JobStatusQueued, JobStatusPending, JobStatusRotating,
	JobStatusSuccess, JobStatusError, JobStatusCanceled,
}

// UnmarshalJSON decodes a job status leniently.
func (s *JobStatus) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) { *s = jobStatusFrom(f) })
}

func jobStatusFrom(f jobFields) JobStatus {
	acquired, _ := f.integer("AcquiredConcurrency")
	limit, _ := f.integer("LimitConcurrency")
	st := JobStatus{
		JobID: f.str("JobID"), TaskIDs: f.strList("TaskIDs"), Status: f.str("Status"),
		StartTime: f.str("StartTime"), EndTime: f.str("EndTime"),
		AcquiredConcurrency: acquired, LimitConcurrency: limit, Canceled: f.boolean("Canceled"),
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
// "GeoCode", "Render", "Output", "WebhookURL"); it is not ScrapeConfig query
// serialization. The body is canonical JSON. targets must be non-empty, and
// options may set neither "Targets" nor "Plugin" (a plugin job is
// CreatePlugin).
func (j *AsyncJobs) Create(ctx context.Context, targets []string, options map[string]any) (*Job, error) {
	if len(targets) == 0 {
		return nil, &ConfigError{Message: "Create needs at least one target URL"}
	}
	return j.create(ctx, "Targets", targets, options)
}

// maxPluginParams is the API's limit on Params entries (tasks) per plugin job.
const maxPluginParams = 1000

// pluginKeyRe is the shape of a documented plugin key, e.g. "amazon/pdp" or
// "google/shopping/product/stores".
var pluginKeyRe = regexp.MustCompile(`^[a-z0-9-]+(/[a-z0-9-]+)+$`)

// CreatePlugin submits a plugin job: key names the plugin (e.g. "amazon/pdp",
// "google/search") and each entry of params becomes one task, with the
// plugin's own parameter names (e.g. {"asin": "B0...", "geocode": "us"}).
// params must hold 1 to 1000 entries. options is merged into the body like
// Create's (e.g. "GeoCode", "WebhookURL", "WebhookHeaders") and may set
// neither "Targets" nor "Plugin": the API rejects a job that has both.
func (j *AsyncJobs) CreatePlugin(ctx context.Context, key string, params []map[string]any, options map[string]any) (*Job, error) {
	if !pluginKeyRe.MatchString(key) {
		return nil, &ConfigError{Message: fmt.Sprintf(
			"invalid plugin key %q: it is a documented plugin path such as amazon/pdp or google/search", key)}
	}
	if len(params) == 0 || len(params) > maxPluginParams {
		return nil, &ConfigError{Message: fmt.Sprintf(
			"a plugin job needs 1 to %d params entries (one per task), got %d", maxPluginParams, len(params))}
	}
	for i, p := range params {
		if p == nil {
			return nil, &ConfigError{Message: fmt.Sprintf("plugin params entry %d is nil", i)}
		}
	}
	return j.create(ctx, "Plugin", map[string]any{"Key": key, "Params": params}, options)
}

// create builds a job body from options plus the job's one source, "Targets"
// or "Plugin", and posts it.
func (j *AsyncJobs) create(ctx context.Context, sourceKey string, source any, options map[string]any) (*Job, error) {
	payload := make(map[string]any, len(options)+1)
	for k, v := range options {
		if strings.EqualFold(k, "Targets") || strings.EqualFold(k, "Plugin") {
			return nil, &ConfigError{Message: fmt.Sprintf(
				"options may not set %s: a job has either targets (Create) or a plugin (CreatePlugin)", k)}
		}
		payload[k] = v
	}
	payload[sourceKey] = source
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

// JobPage is one page of List's result. TotalCount and TotalPages count every
// job matching the filters; PageSize and PageNumber echo the page served.
type JobPage struct {
	Jobs       []JobStatus `json:"Jobs"`
	TotalCount int         `json:"TotalCount"`
	PageSize   int         `json:"PageSize"`
	PageNumber int         `json:"PageNumber"`
	TotalPages int         `json:"TotalPages"`
}

// UnmarshalJSON decodes a job page leniently.
func (p *JobPage) UnmarshalJSON(data []byte) error {
	return decodeLenient(data, func(f jobFields) {
		page := JobPage{}
		for _, s := range f.objects("Jobs") {
			page.Jobs = append(page.Jobs, jobStatusFrom(s))
		}
		page.TotalCount, _ = f.integer("TotalCount")
		page.PageSize, _ = f.integer("PageSize")
		page.PageNumber, _ = f.integer("PageNumber")
		page.TotalPages, _ = f.integer("TotalPages")
		*p = page
	})
}

// The sort orders List accepts.
const (
	JobSortStartTimeDesc = "start_time_desc" // newest first, the API default
	JobSortStartTimeAsc  = "start_time_asc"
)

// maxJobPageSize is the API's largest page.
const maxJobPageSize = 100

// ListJobsOptions filters and pages List. Every zero field is left to the API's
// default: page 1, 10 jobs a page, every status, no date bounds, newest first.
type ListJobsOptions struct {
	// Page is the 1-based page number.
	Page int
	// PageSize is the number of jobs a page, 1 to 100.
	PageSize int
	// Status keeps only jobs in that state (one of the JobStatus* values).
	Status string
	// StartFrom and StartTo bound the job's start time, inclusive. They are
	// sent as RFC 3339 in UTC.
	StartFrom, StartTo time.Time
	// Sort is JobSortStartTimeDesc or JobSortStartTimeAsc.
	Sort string
}

// query validates the options and renders the List query string.
func (o *ListJobsOptions) query() (string, error) {
	if o == nil {
		return "", nil
	}
	q := url.Values{}
	if o.Page < 0 {
		return "", &ConfigError{Message: fmt.Sprintf("Page must be 1 or more, got %d", o.Page)}
	} else if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	if o.PageSize < 0 || o.PageSize > maxJobPageSize {
		return "", &ConfigError{Message: fmt.Sprintf("PageSize must be 1 to %d, got %d", maxJobPageSize, o.PageSize)}
	} else if o.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(o.PageSize))
	}
	if o.Status != "" {
		if !slices.Contains(jobStatuses, o.Status) {
			return "", &ConfigError{Message: fmt.Sprintf(
				"Status %q is not a job status (%s)", o.Status, strings.Join(jobStatuses, ", "))}
		}
		q.Set("status", o.Status)
	}
	if !o.StartFrom.IsZero() {
		q.Set("start_from", o.StartFrom.UTC().Format(time.RFC3339))
	}
	if !o.StartTo.IsZero() {
		q.Set("start_to", o.StartTo.UTC().Format(time.RFC3339))
	}
	switch o.Sort {
	case "":
	case JobSortStartTimeDesc, JobSortStartTimeAsc:
		q.Set("sort", o.Sort)
	default:
		return "", &ConfigError{Message: fmt.Sprintf(
			"Sort %q is neither %s nor %s", o.Sort, JobSortStartTimeDesc, JobSortStartTimeAsc)}
	}
	if len(q) == 0 {
		return "", nil
	}
	return "?" + q.Encode(), nil
}

// List returns one page of the caller's jobs; opts filters and pages it (nil
// takes the API's defaults: the 10 most recent jobs). Walk every page with
// opts.Page from 1 to the returned TotalPages.
func (j *AsyncJobs) List(ctx context.Context, opts *ListJobsOptions) (*JobPage, error) {
	query, err := opts.query()
	if err != nil {
		return nil, err
	}
	var page JobPage
	if err := j.do(ctx, http.MethodGet, "/jobs"+query, nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
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
