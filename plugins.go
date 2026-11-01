// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// pluginRouteRe validates a caller-supplied plugin route before it enters the
// URL path. Routes reach this from the MCP server, where an LLM chooses the
// value, so traversal ("../"), query injection ("x?token=") and fragment
// truncation ("x#") must all be rejected client-side. The whole string must
// match; nothing is trimmed or normalized first.
var pluginRouteRe = regexp.MustCompile(`^[a-z0-9]+(?:[/-][a-z0-9]+)*$`)

// pluginRoutePaths maps a route to the path the gateway serves it under when
// the two differ. The Amazon raw-HTML plugin is registered as /plugin/amazon/
// (the group root), and the gateway answers /plugin/amazon with a 301 to it,
// which the SDK never follows; its documented spelling "amazon/" is accepted
// too.
var pluginRoutePaths = map[string]string{"amazon": "amazon/", "amazon/": "amazon/"}

// routePath validates route and returns the path under /plugin/ to call.
func routePath(route string) (string, error) {
	if p, ok := pluginRoutePaths[route]; ok {
		return p, nil
	}
	if !pluginRouteRe.MatchString(route) {
		return "", &ConfigError{Message: fmt.Sprintf(
			"invalid plugin route %q: routes look like \"google/shopping\" — "+
				"lowercase letters and digits, separated by / or -", route)}
	}
	return route, nil
}

// pluginParams serializes plugin params: keys sorted, a list repeats its key
// per element, booleans literal ("true"/"false", unlike ScrapeConfig, where
// false is omitted), numbers in ECMAScript form, nil skipped, strings
// verbatim, objects as canonical JSON. An empty key, the key "token" (in any
// case: the token comes from the client), a key outside
// ^[A-Za-z][A-Za-z0-9_.-]*$ and a list inside a list are *ConfigErrors.
func pluginParams(params map[string]any) ([]param, error) {
	var out []param
	for _, k := range sortedKeys(params) {
		if k == "" {
			return nil, &ConfigError{Message: "plugin params may not have an empty key"}
		}
		if strings.EqualFold(k, "token") {
			return nil, &ConfigError{Message: `plugin params may not set "token": the token comes from the client`}
		}
		if !validParamKey(k) {
			return nil, &ConfigError{Message: fmt.Sprintf("plugin params key %q "+paramKeyRule, k)}
		}
		v := params[k]
		if !isList(v) {
			s, ok, err := pluginScalar(k, v)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, param{k, s})
			}
			continue
		}
		rv := reflect.ValueOf(v)
		for i := 0; i < rv.Len(); i++ {
			e := rv.Index(i).Interface()
			if isList(e) {
				return nil, &ConfigError{Message: fmt.Sprintf("plugin param %q is a list inside a list; send it as a string", k)}
			}
			s, ok, err := pluginScalar(k, e)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, param{k, s})
			}
		}
	}
	return out, nil
}

// isList reports whether v is a slice or array (a []byte is a scalar).
func isList(v any) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	return (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && t.Elem().Kind() != reflect.Uint8
}

// pluginScalar formats one plugin value; ok is false for nil (skipped).
func pluginScalar(key string, v any) (string, bool, error) {
	if v == nil {
		return "", false, nil
	}
	if s, isString := v.(string); isString {
		return s, true, nil
	}
	s, err := canonicalJSON(v)
	if err != nil {
		return "", false, &ConfigError{Message: fmt.Sprintf("plugin param %q is not serializable: %v", key, err)}
	}
	// A value that encodes as a JSON string (a named string type, a time) is
	// sent as that string, not as a quoted literal.
	var str string
	if strings.HasPrefix(s, `"`) && json.Unmarshal([]byte(s), &str) == nil {
		return str, true, nil
	}
	return s, s != "null", nil
}

// pluginTimeoutMs is the plugin's own "timeout" param (milliseconds) when it
// is sent once as a positive integer, else 0. Like ScrapeConfig.Timeout, it
// raises the attempt's deadline to the gateway timeout plus a margin.
func pluginTimeoutMs(params []param) int {
	ms, found := 0, false
	for _, p := range params {
		if p.Key != "timeout" {
			continue
		}
		if found || !allDigits(p.Value) {
			return 0
		}
		n, err := strconv.Atoi(p.Value)
		if err != nil {
			return 0
		}
		ms, found = n, true
	}
	return ms
}

// pluginResult calls /plugin/<route> generically and returns the delivered
// response, headers and status included.
func pluginResult(ctx context.Context, t *transport, baseURL, route string, params map[string]any) (*Result, error) {
	path, err := routePath(route)
	if err != nil {
		return nil, err
	}
	extra, err := pluginParams(params)
	if err != nil {
		return nil, err
	}
	query := encodeQuery(append([]param{{"token", t.token()}}, extra...))
	in, err := t.do(ctx, outgoing{
		method:           http.MethodGet,
		url:              baseURL + "/plugin/" + path + "?" + query,
		gatewayTimeoutMs: pluginTimeoutMs(extra),
	})
	if err != nil {
		return nil, err
	}
	return &Result{content: in.body, StatusCode: in.status, Header: in.header}, nil
}

// pluginOutput is a plugin result's payload: parsed JSON (any) when the
// Content-Type says JSON, else the raw bytes ([]byte).
func pluginOutput(t *transport, res *Result) (any, error) {
	if strings.Contains(strings.ToLower(res.ContentType()), "json") {
		var out any
		if err := unmarshalBody(res.content, &out); err != nil {
			return nil, &TransportError{Message: "plugin response is not valid JSON", Err: t.cause(err)}
		}
		return out, nil
	}
	return res.content, nil
}

// callPlugin builds /plugin/<route> generically and returns parsed JSON (any) or
// raw bytes ([]byte) for non-JSON plugins.
func callPlugin(ctx context.Context, t *transport, baseURL, route string, params map[string]any) (any, error) {
	res, err := pluginResult(ctx, t, baseURL, route, params)
	if err != nil {
		return nil, err
	}
	return pluginOutput(t, res)
}
