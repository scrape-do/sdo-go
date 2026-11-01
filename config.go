// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// param is one serialized query key/value pair.
type param struct {
	Key   string
	Value string
}

// Enum values, lowercase. Input is matched case-insensitively and sent
// lowercase, so "Mobile" and "mobile" are the same request in every SDK.
var (
	renderModes = []string{"true", "false"}
	devices     = []string{"desktop", "mobile", "tablet"}
	waitUntils  = []string{"domcontentloaded", "load", "networkidle0", "networkidle2"}
	outputs     = []string{"raw", "markdown"}
)

// ScrapeConfig is the single, serializable request spec.
//
// A zero value means "unset" and is omitted from the wire: false, 0, "" and an
// empty slice or map. Unset values are never validated. Use ExtraParams to send
// a literal the typed fields cannot express (for example key=false).
type ScrapeConfig struct {
	// URL is the target: an absolute http or https URL. It is sent exactly as
	// given.
	URL string

	// rendering / proxy

	// Render enables JS rendering (render=true).
	Render bool
	// RenderMode is the render enum: "true" or "false" (matched
	// case-insensitively, sent lowercase). When set it wins over Render.
	// "false" disables rendering and, like a false Render, is not sent.
	// Render: true together with RenderMode "false" is a *ConfigError.
	RenderMode string
	SuperProxy bool   // wire: super
	Device     string // desktop | mobile | tablet

	// timeouts (gateway-side, milliseconds)

	Timeout int // 5000–120000 ms
	// RetryTimeout is 5000–55000 ms and cannot be combined with rendering
	// (the gateway rejects render with retryTimeout).
	RetryTimeout int
	DisableRetry bool

	// geo
	GeoCode string
	// RegionalGeoCode routes through a random country of a region (europe,
	// asia, africa, oceania, northamerica, southamerica). It requires
	// SuperProxy and cannot be combined with GeoCode: the gateway rejects
	// both, so they are a *ConfigError here.
	RegionalGeoCode string
	// ZipCode targets a postal code. It requires SuperProxy and GeoCode (the
	// gateway silently ignores it without super, routing through datacenter
	// proxies), so a ZipCode without them is a *ConfigError.
	ZipCode string

	// session / cookies / headers

	// SessionID keeps the same proxy (IP) across requests: an integer
	// 0-1000000 in decimal digits (at most 7 of them, the gateway's limit).
	// Anything else is a *ConfigError. "" is unset; "0" is a valid id.
	SessionID string
	// SetCookies is the RAW cookie string ("name=value; name2=value2"). The
	// gateway expects it URL-encoded and the SDK encodes it as a query value, so
	// pre-encoding it yourself double-encodes and breaks it.
	SetCookies string
	// CustomHeaders, ExtraHeaders and ForwardHeaders choose how Headers reach
	// the target; at most one may be set (the gateway rejects any two).
	CustomHeaders  bool
	ExtraHeaders   bool
	ForwardHeaders bool
	// Headers are sent with the request; Request.Headers win over them.
	// Whether the gateway forwards them to the target is controlled by
	// CustomHeaders / ExtraHeaders / ForwardHeaders. Names are
	// case-insensitive and must be RFC 9110 tokens; values printable ASCII
	// plus space and tab. Host, Content-Length, Transfer-Encoding and
	// Connection cannot be set. Anything else is a *ConfigError before the
	// request is sent.
	Headers map[string]string

	// render-only behaviour: each requires rendering (Render or RenderMode)
	CustomWait   int    // 0–35000 ms
	WaitSelector string // CSS selector
	WaitUntil    string // domcontentloaded | load | networkidle0 | networkidle2
	// BlockResources is tri-state. nil leaves it unset and sends nothing, so
	// the gateway applies its default of true (resources blocked). A *false
	// is sent explicitly (blockResources=false) to load resources — omitting
	// it would leave them wrongly blocked by that default. A *true is sent as
	// blockResources=true and, being render-only, requires rendering; a *false
	// does not. Use sdo.Bool to take the address of a literal.
	BlockResources  *bool
	PlayWithBrowser []map[string]any
	// ShowWebsocketRequests and ShowFrames add WebSocket messages and iframe
	// content to the JSON response: each requires ReturnJSON as well.
	ShowWebsocketRequests bool
	ShowFrames            bool

	// screenshots (render-only, and each requires ReturnJSON: the image comes
	// back base64-encoded inside the JSON response). At most one of the three
	// may be set, and ParticularScreenshot cannot be combined with
	// PlayWithBrowser (take the screenshot as a ScreenShot action instead).
	Screenshot           bool
	FullScreenshot       bool
	ParticularScreenshot string // CSS selector
	// Height and Width set the browser viewport in px (render-only).
	Height int
	Width  int

	// output / behaviour
	Output string // raw | markdown
	// ReturnJSON returns the network requests and page content as JSON. With
	// rendering it carries the browser's XHR/fetch requests (and screenshots,
	// frames, WebSocket messages when asked for); without rendering the gateway
	// wraps the plain response in the same JSON envelope.
	ReturnJSON bool
	// PureCookies returns the target's original Set-Cookie header instead of
	// the Scrape.do-Cookies header. The body is unaffected.
	PureCookies         bool
	DisableRedirection  bool
	TransparentResponse bool

	// misc
	// ExtraParams is the escape hatch for gateway parameters without a typed
	// field. Keys are sent after the typed params, sorted, with values
	// verbatim. "token" and "url" are reserved (case-insensitively), and a key
	// must be an ASCII letter followed by letters, digits, '_', '.' or '-'.
	ExtraParams map[string]string
}

// renderValue resolves Render and RenderMode into the one render enum value:
// "" (unset), "true" or "false".
func (c *ScrapeConfig) renderValue() (string, error) {
	if c.RenderMode == "" {
		if c.Render {
			return "true", nil
		}
		return "", nil
	}
	mode, err := enumValue("RenderMode", c.RenderMode, renderModes)
	if err != nil {
		return "", err
	}
	if c.Render && mode == "false" {
		return "", &ConfigError{Message: `Render is true but RenderMode is "false"; set only one of them`}
	}
	return mode, nil
}

// enumValue validates v case-insensitively against allowed and returns it
// lowercased. An empty v is unset.
func enumValue(field, v string, allowed []string) (string, error) {
	if v == "" {
		return "", nil
	}
	lower := lowerASCII(v)
	for _, a := range allowed {
		if lower == a {
			return lower, nil
		}
	}
	return "", &ConfigError{Message: fmt.Sprintf(
		"%s must be one of %s (case-insensitive), got %q", field, strings.Join(allowed, "|"), v)}
}

// checkRange validates a millisecond value against its documented range. Zero
// is unset and never validated.
func checkRange(field string, v, lo, hi int) error {
	if v == 0 || (v >= lo && v <= hi) {
		return nil
	}
	return &ConfigError{Message: fmt.Sprintf(
		"%s must be between %d and %d milliseconds, got %d", field, lo, hi, v)}
}

// validTargetURL reports whether u is an absolute http(s) URL with a host. It
// checks the string itself, not what a lenient URL parser makes of it: scheme
// http or https (any case), "://", then an authority up to the first /, ? or #
// (optional userinfo@, a non-empty host, an optional port 1-65535), and no
// whitespace, control character or backslash anywhere. The rest of the URL is
// the gateway's business; it is forwarded exactly as given.
func validTargetURL(u string) bool {
	_, authority, _, ok := splitHTTPURL(u)
	if !ok {
		return false
	}
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		authority = authority[i+1:]
	}
	_, ok = splitHostPort(authority)
	return ok
}

// shownURL is a URL as an error message may show it: any userinfo
// ("user:password@") is masked and the credential shapes RedactSecrets knows
// are redacted.
func shownURL(u string) string {
	if scheme, after, found := strings.Cut(u, "://"); found {
		end := strings.IndexAny(after, "/?#")
		if end < 0 {
			end = len(after)
		}
		if at := strings.LastIndexByte(after[:end], '@'); at >= 0 {
			u = scheme + "://" + redactMask + after[at:]
		}
	}
	return RedactSecrets(u)
}

// splitHTTPURL splits an http(s) URL into its lowercased scheme, its authority
// (up to the first /, ? or #) and the rest, rejecting whitespace, control
// characters and backslashes anywhere.
func splitHTTPURL(u string) (scheme, authority, rest string, ok bool) {
	if strings.ContainsFunc(u, func(r rune) bool { return r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", "", "", false
	}
	scheme, after, found := strings.Cut(u, "://")
	scheme = lowerASCII(scheme)
	if !found || (scheme != "http" && scheme != "https") {
		return "", "", "", false
	}
	authority, rest = after, ""
	if i := strings.IndexAny(after, "/?#"); i >= 0 {
		authority, rest = after[:i], after[i:]
	}
	return scheme, authority, rest, true
}

// splitHostPort returns the host of "host[:port]" (an IPv6 literal keeps no
// brackets) and whether the host is non-empty and the port, when present, is
// 1-65535.
func splitHostPort(hostport string) (string, bool) {
	host, port := hostport, ""
	if strings.HasPrefix(hostport, "[") {
		end := strings.IndexByte(hostport, ']')
		if end < 0 {
			return "", false
		}
		host, port = hostport[1:end], hostport[end+1:]
		if port != "" {
			if port[0] != ':' {
				return "", false
			}
			port = port[1:]
		}
	} else if i := strings.LastIndexByte(hostport, ':'); i >= 0 {
		host, port = hostport[:i], hostport[i+1:]
	}
	if host == "" {
		return "", false
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || !allDigits(port) || n < 1 || n > 65535 {
			return "", false
		}
	}
	return host, true
}

// renderOnlySet names the first render-only field that carries a value, or "".
func (c *ScrapeConfig) renderOnlySet() string {
	switch {
	case c.CustomWait != 0:
		return "CustomWait"
	case c.WaitSelector != "":
		return "WaitSelector"
	case c.WaitUntil != "":
		return "WaitUntil"
	case c.BlockResources != nil && *c.BlockResources:
		return "BlockResources"
	case len(c.PlayWithBrowser) > 0:
		return "PlayWithBrowser"
	case c.ShowWebsocketRequests:
		return "ShowWebsocketRequests"
	case c.ShowFrames:
		return "ShowFrames"
	case c.Screenshot:
		return "Screenshot"
	case c.FullScreenshot:
		return "FullScreenshot"
	case c.ParticularScreenshot != "":
		return "ParticularScreenshot"
	case c.Height != 0:
		return "Height"
	case c.Width != 0:
		return "Width"
	}
	return ""
}

// maxSessionID is the largest documented sessionId.
const maxSessionID = 1_000_000

// validSessionID reports whether s is a documented sessionId: decimal digits
// (at most 7, the gateway's length limit) naming an integer 0-1000000.
func validSessionID(s string) bool {
	if len(s) > 7 || !allDigits(s) {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n <= maxSessionID
}

// checkDependencies applies the documented rules that tie parameters
// together, each of which the gateway enforces with a 400 (or, for ZipCode
// and ParticularScreenshot with PlayWithBrowser, documents as invalid), so the
// mistake costs nothing. rendering is whether render is on.
func (c *ScrapeConfig) checkDependencies(rendering bool) error {
	bad := func(format string, args ...any) error {
		return &ConfigError{Message: fmt.Sprintf(format, args...)}
	}
	if c.SessionID != "" && !validSessionID(c.SessionID) {
		return bad("SessionID must be an integer from 0 to 1000000 in decimal digits, got %q", c.SessionID)
	}
	if c.RegionalGeoCode != "" {
		if !c.SuperProxy {
			return bad("RegionalGeoCode requires SuperProxy")
		}
		if c.GeoCode != "" {
			return bad("RegionalGeoCode and GeoCode cannot be combined; set only one of them")
		}
	}
	if c.ZipCode != "" && (!c.SuperProxy || c.GeoCode == "") {
		return bad("ZipCode requires SuperProxy and a GeoCode (the country of the postal code)")
	}
	headerModes := 0
	for _, on := range []bool{c.CustomHeaders, c.ExtraHeaders, c.ForwardHeaders} {
		if on {
			headerModes++
		}
	}
	if headerModes > 1 {
		return bad("CustomHeaders, ExtraHeaders and ForwardHeaders cannot be combined; set only one of them")
	}
	if c.RetryTimeout != 0 && rendering {
		return bad("RetryTimeout cannot be combined with rendering")
	}
	shots := 0
	for _, on := range []bool{c.Screenshot, c.FullScreenshot, c.ParticularScreenshot != ""} {
		if on {
			shots++
		}
	}
	if shots > 1 {
		return bad("Screenshot, FullScreenshot and ParticularScreenshot cannot be combined; set only one of them")
	}
	if !c.ReturnJSON {
		for _, f := range []struct {
			name string
			on   bool
		}{
			{"ShowWebsocketRequests", c.ShowWebsocketRequests}, {"ShowFrames", c.ShowFrames},
			{"Screenshot", c.Screenshot}, {"FullScreenshot", c.FullScreenshot},
			{"ParticularScreenshot", c.ParticularScreenshot != ""},
		} {
			if f.on {
				return bad("%s requires ReturnJSON (its output is part of the JSON response)", f.name)
			}
		}
	}
	if c.ParticularScreenshot != "" && len(c.PlayWithBrowser) > 0 {
		return bad("ParticularScreenshot cannot be combined with PlayWithBrowser; " +
			`add a {"Action": "ScreenShot", "particularScreenShot": "<selector>"} action instead`)
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Bool returns a pointer to b, for setting tri-state *bool fields such as
// ScrapeConfig.BlockResources from a literal (Go cannot take the address of
// one): BlockResources: sdo.Bool(false).
func Bool(b bool) *bool { return &b }

// toParams validates the config and serializes it to the ordered wire pairs:
// url, the typed params in spec order, then ExtraParams sorted by key.
func (c *ScrapeConfig) toParams() ([]param, error) {
	if c.URL == "" {
		return nil, &ConfigError{Message: "ScrapeConfig requires a non-empty URL."}
	}
	if !validTargetURL(c.URL) {
		return nil, &ConfigError{Message: fmt.Sprintf(
			"ScrapeConfig.URL must be an absolute http or https URL with a host, got %q", shownURL(c.URL))}
	}
	render, err := c.renderValue()
	if err != nil {
		return nil, err
	}
	device, err := enumValue("Device", c.Device, devices)
	if err != nil {
		return nil, err
	}
	waitUntil, err := enumValue("WaitUntil", c.WaitUntil, waitUntils)
	if err != nil {
		return nil, err
	}
	output, err := enumValue("Output", c.Output, outputs)
	if err != nil {
		return nil, err
	}
	for _, r := range []struct {
		field     string
		v, lo, hi int
	}{
		{"Timeout", c.Timeout, 5000, 120000},
		{"RetryTimeout", c.RetryTimeout, 5000, 55000},
		{"CustomWait", c.CustomWait, 0, 35000},
	} {
		if err := checkRange(r.field, r.v, r.lo, r.hi); err != nil {
			return nil, err
		}
	}
	rendering := render == "true"
	if name := c.renderOnlySet(); name != "" && !rendering {
		return nil, &ConfigError{Message: fmt.Sprintf(
			"%s only applies to rendered requests: set Render: true", name)}
	}
	if err := c.checkDependencies(rendering); err != nil {
		return nil, err
	}
	for k := range c.ExtraParams {
		if k == "" {
			return nil, &ConfigError{Message: "ExtraParams may not have an empty key"}
		}
		if strings.EqualFold(k, "token") || strings.EqualFold(k, "url") {
			return nil, &ConfigError{Message: fmt.Sprintf(
				"ExtraParams may not set %q: the token comes from the client and url from ScrapeConfig.URL", k)}
		}
		if !validParamKey(k) {
			return nil, &ConfigError{Message: fmt.Sprintf("ExtraParams key %q "+paramKeyRule, k)}
		}
	}

	out := []param{{"url", c.URL}}
	addBool := func(k string, v bool) {
		if v {
			out = append(out, param{k, "true"})
		}
	}
	addStr := func(k, v string) {
		if v != "" {
			out = append(out, param{k, v})
		}
	}
	addInt := func(k string, v int) {
		if v != 0 {
			out = append(out, param{k, strconv.Itoa(v)})
		}
	}
	addJSON := func(k, field string, v any) error {
		s, err := canonicalJSON(v)
		if err != nil {
			return &ConfigError{Message: fmt.Sprintf("%s is not JSON-serializable: %v", field, err)}
		}
		out = append(out, param{k, s})
		return nil
	}

	if rendering {
		out = append(out, param{"render", render})
	}
	addBool("super", c.SuperProxy)
	addStr("device", device)
	addInt("timeout", c.Timeout)
	addInt("retryTimeout", c.RetryTimeout)
	addBool("disableRetry", c.DisableRetry)
	addStr("geoCode", c.GeoCode)
	addStr("regionalGeoCode", c.RegionalGeoCode)
	addStr("zipCode", c.ZipCode)
	addStr("sessionId", c.SessionID)
	addStr("setCookies", c.SetCookies)
	addBool("customHeaders", c.CustomHeaders)
	addBool("extraHeaders", c.ExtraHeaders)
	addBool("forwardHeaders", c.ForwardHeaders)
	addInt("customWait", c.CustomWait)
	addStr("waitSelector", c.WaitSelector)
	addStr("waitUntil", waitUntil)
	if c.BlockResources != nil {
		out = append(out, param{"blockResources", boolStr(*c.BlockResources)})
	}
	if len(c.PlayWithBrowser) > 0 {
		if err := addJSON("playWithBrowser", "PlayWithBrowser", c.PlayWithBrowser); err != nil {
			return nil, err
		}
	}
	addBool("showWebsocketRequests", c.ShowWebsocketRequests)
	addBool("showFrames", c.ShowFrames)
	addBool("screenShot", c.Screenshot)
	addBool("fullScreenShot", c.FullScreenshot)
	addStr("particularScreenShot", c.ParticularScreenshot)
	addInt("height", c.Height)
	addInt("width", c.Width)
	addStr("output", output)
	addBool("returnJSON", c.ReturnJSON)
	addBool("pureCookies", c.PureCookies)
	addBool("disableRedirection", c.DisableRedirection)
	addBool("transparentResponse", c.TransparentResponse)
	// Sorted so the query string is byte-reproducible: Go randomizes map
	// iteration.
	for _, k := range sortedKeys(c.ExtraParams) {
		out = append(out, param{k, c.ExtraParams[k]})
	}
	return out, nil
}

// paramKeyRule completes the *ConfigError for a key validParamKey refuses.
const paramKeyRule = "is not a valid parameter name: use ASCII letters, digits, '_', '.' or '-', starting with a letter"

// validParamKey reports whether k matches ^[A-Za-z][A-Za-z0-9_.-]*$, the shape
// of every documented gateway and plugin parameter. Anything else (a '&', '=',
// '[', space or non-ASCII rune) is a typo or an attempt to smuggle a second
// parameter past the reserved-key check, e.g. "token " or "url[]".
func validParamKey(k string) bool {
	if k == "" || !isASCIILetter(k[0]) {
		return false
	}
	for i := 1; i < len(k); i++ {
		c := k[i]
		if !isASCIILetter(c) && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}
