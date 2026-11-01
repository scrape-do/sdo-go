// SPDX-License-Identifier: Apache-2.0

package sdo_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	sdo "github.com/scrape-do/sdo-go"
)

// ExampleNew shows constructing a client. The token is read from the explicit
// argument, falling back to the SDO_TOKEN environment variable.
func ExampleNew() {
	client, err := sdo.New("your-token")
	if err != nil {
		fmt.Println("config error:", err)
		return
	}
	_ = client
}

// ExampleNew_options configures timeouts, retries, and brotli negotiation.
func ExampleNew_options() {
	client, err := sdo.New(
		"your-token",
		sdo.WithTimeout(30*time.Second),
		sdo.WithRetries(3),
		sdo.WithBackoff(250*time.Millisecond),
		sdo.WithPreferBrotli(),
	)
	if err != nil {
		fmt.Println("config error:", err)
		return
	}
	_ = client
}

// ExampleClient_Scrape performs a basic scrape and reads the decoded body.
func ExampleClient_Scrape() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	res, err := client.Scrape(context.Background(), &sdo.ScrapeConfig{
		URL: "https://example.com",
	})
	if err != nil {
		return
	}
	fmt.Println(res.StatusCode)
	fmt.Println(res.Text())
}

// ExampleClient_Scrape_render enables headless rendering with a geo target and
// a wait condition — the render-only fields require Render (or RenderMode) set.
func ExampleClient_Scrape_render() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	res, err := client.Scrape(context.Background(), &sdo.ScrapeConfig{
		URL:          "https://example.com",
		Render:       true,
		GeoCode:      "us",
		WaitUntil:    "networkidle2",
		WaitSelector: "#app",
		Output:       "markdown",
	})
	if err != nil {
		return
	}
	fmt.Println(res.Text())
}

// ExampleScrapeConfig demonstrates the request spec and error inspection. A
// gateway-origin failure is an *sdo.APIError whose Kind classifies the cause.
func ExampleScrapeConfig() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	cfg := &sdo.ScrapeConfig{
		URL:        "https://httpbin.org/json",
		SuperProxy: true,
		Device:     "desktop",
	}
	if _, err := client.Scrape(context.Background(), cfg); err != nil {
		var apiErr *sdo.APIError
		// An exhausted credit balance arrives as an auth error: the gateway has
		// no 402 tier, so the message is what tells the two apart.
		if errors.As(err, &apiErr) && apiErr.IsAuth() {
			fmt.Println("auth failed or out of monthly credits:", apiErr.Message)
		}
	}
}

// ExampleClient_Plugin calls a structured-data plugin endpoint. JSON responses
// come back as a decoded any; non-JSON plugins return raw []byte.
func ExampleClient_Plugin() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	out, err := client.Plugin(context.Background(), "google/shopping", map[string]any{
		"q":       "laptop",
		"geoCode": "us",
	})
	if err != nil {
		return
	}
	fmt.Printf("%T\n", out)
}

// ExampleClient_Do sends a POST with a JSON body through the gateway. The body
// is canonical JSON; a Request with no Method and a body is a POST.
func ExampleClient_Do() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	res, err := client.Do(context.Background(),
		&sdo.ScrapeConfig{URL: "https://httpbin.org/post", CustomHeaders: true},
		&sdo.Request{
			JSON:    map[string]any{"query": "laptop", "page": 1},
			Headers: map[string]string{"Accept": "application/json"},
		})
	if err != nil {
		return
	}
	fmt.Println(res.StatusCode, res.Header.Values("Set-Cookie"))
}

// ExampleWithUserAgent is for tools built on the SDK: the value must be a
// Scrape.do client identifier, or New returns a *ConfigError.
func ExampleWithUserAgent() {
	_, err := sdo.New("your-token", sdo.WithUserAgent("sdo-cli/1.0.0"))
	fmt.Println(err)
	_, err = sdo.New("your-token", sdo.WithUserAgent("my-bot/1.0"))
	var cfgErr *sdo.ConfigError
	fmt.Println(errors.As(err, &cfgErr))
	// Output:
	// <nil>
	// true
}

// ExampleClient_Jobs submits an async job via the REST job API, polls it and
// reads a task's content. The options map uses the job API's own PascalCase
// body keys (e.g. Super, GeoCode).
func ExampleClient_Jobs() {
	client, err := sdo.New("your-token")
	if err != nil {
		return
	}
	ctx := context.Background()
	job, err := client.Jobs().Create(ctx, []string{"https://example.com"},
		map[string]any{"Super": true, "GeoCode": "us"})
	if err != nil {
		return
	}
	status, err := client.Jobs().Status(ctx, job.JobID)
	if err != nil || len(status.Tasks) == 0 {
		return
	}
	task, err := client.Jobs().Result(ctx, job.JobID, status.Tasks[0].TaskID)
	if err != nil {
		return
	}
	// Binary targets arrive base64-encoded; ContentBytes decodes them.
	body, err := task.ContentBytes()
	if err != nil {
		return
	}
	fmt.Println(task.Status, task.StatusCode, len(body))
}

// Example_quickstart is the README quickstart, run against a local stand-in
// for the gateway that returns a page shorter than 200 bytes.
func Example_quickstart() {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Scrape.do-Request-Cost", "5")
		fmt.Fprint(w, "tiny page")
	}))
	defer gateway.Close()

	client, err := sdo.New("YOUR_TOKEN", sdo.WithBaseURL(gateway.URL))
	if err != nil {
		panic(err)
	}
	res, err := client.Scrape(context.Background(), &sdo.ScrapeConfig{
		URL:     "https://example.com",
		Render:  true,
		GeoCode: "us",
	})
	if err != nil {
		panic(err)
	}
	cost, _ := res.Cost()
	fmt.Println(res.StatusCode, cost)
	text := res.Text()
	fmt.Println(text[:min(200, len(text))]) // the first 200 bytes, or fewer
	// Output:
	// 200 5
	// tiny page
}
