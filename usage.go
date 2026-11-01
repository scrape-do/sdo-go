// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Usage is the account's usage snapshot from the /info endpoint. When the
// subscription is not active, the gateway reports every count as 0.
type Usage struct {
	// IsActive reports whether the subscription is active.
	IsActive bool
	// ConcurrentRequest is the plan's concurrent-request limit.
	ConcurrentRequest int
	// MaxMonthlyRequest is the plan's monthly request limit.
	MaxMonthlyRequest int
	// RemainingConcurrentRequest is how many more requests may run right now.
	RemainingConcurrentRequest int
	// RemainingMonthlyRequest is how many requests are left this month.
	RemainingMonthlyRequest int
}

// usageFields are the /info keys, all required.
var usageFields = []string{
	"IsActive", "ConcurrentRequest", "MaxMonthlyRequest", "RemainingConcurrentRequest", "RemainingMonthlyRequest",
}

// Usage returns the account's usage statistics (GET <base URL>/info, the token
// as a query parameter). It costs no credits.
//
// The endpoint allows 10 requests per minute per IP address and answers 429
// above that, so a retryable status is not retried here (a retry within
// seconds would only spend more of the same minute); a failure before the
// request was sent still is. A token the gateway does not know is answered
// with a 502 "authentication refused".
func (c *Client) Usage(ctx context.Context) (*Usage, error) {
	in, err := c.t.do(ctx, outgoing{
		method:        http.MethodGet,
		url:           c.baseURL + "/info?" + encodeQuery([]param{{"token", c.t.token()}}),
		noStatusRetry: true,
	})
	if err != nil {
		return nil, err
	}
	return parseUsage(in.body)
}

// parseUsage reads the /info body: a JSON object carrying every usage field,
// IsActive a boolean and the counts integers.
func parseUsage(body []byte) (*Usage, error) {
	invalid := func(why string) error {
		return &TransportError{Message: "the usage response is not the expected JSON object: " + why}
	}
	var raw map[string]json.RawMessage
	if err := unmarshalBody(body, &raw); err != nil || raw == nil {
		return nil, invalid("not a JSON object")
	}
	for _, k := range usageFields {
		// json.Unmarshal leaves the destination alone for a null, so a null
		// is refused here rather than read as false or 0.
		if v, ok := raw[k]; !ok || string(v) == "null" {
			return nil, invalid(fmt.Sprintf("%s is missing", k))
		}
	}
	var u Usage
	if err := json.Unmarshal(raw["IsActive"], &u.IsActive); err != nil {
		return nil, invalid("IsActive is not a boolean")
	}
	for _, f := range []struct {
		key string
		dst *int
	}{
		{"ConcurrentRequest", &u.ConcurrentRequest},
		{"MaxMonthlyRequest", &u.MaxMonthlyRequest},
		{"RemainingConcurrentRequest", &u.RemainingConcurrentRequest},
		{"RemainingMonthlyRequest", &u.RemainingMonthlyRequest},
	} {
		if err := json.Unmarshal(raw[f.key], f.dst); err != nil {
			return nil, invalid(f.key + " is not an integer")
		}
	}
	return &u, nil
}
