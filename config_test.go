// SPDX-License-Identifier: Apache-2.0

package sdo

import (
	"errors"
	"testing"
)

func paramsMap(t *testing.T, c *ScrapeConfig) map[string][]string {
	t.Helper()
	pairs, err := c.toParams()
	if err != nil {
		t.Fatalf("toParams error: %v", err)
	}
	m := map[string][]string{}
	for _, p := range pairs {
		m[p.Key] = append(m[p.Key], p.Value)
	}
	return m
}

func first(m map[string][]string, k string) string {
	if v, ok := m[k]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

func TestRequiresURL(t *testing.T) {
	_, err := (&ScrapeConfig{}).toParams()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigError, got %v", err)
	}
}

func TestMinimalSerialization(t *testing.T) {
	m := paramsMap(t, &ScrapeConfig{URL: "https://example.com"})
	if len(m) != 1 || first(m, "url") != "https://example.com" {
		t.Fatalf("unexpected params: %v", m)
	}
}

func TestBoolsAndWireNames(t *testing.T) {
	m := paramsMap(t, &ScrapeConfig{URL: "https://u.test", SuperProxy: true, GeoCode: "us", ReturnJSON: true, SetCookies: "a=b"})
	if first(m, "super") != "true" {
		t.Errorf("super: %v", m["super"])
	}
	if first(m, "geoCode") != "us" {
		t.Errorf("geoCode: %v", m["geoCode"])
	}
	if first(m, "returnJSON") != "true" {
		t.Errorf("returnJSON: %v", m["returnJSON"])
	}
	if first(m, "setCookies") != "a=b" {
		t.Errorf("setCookies: %v", m["setCookies"])
	}
}

func TestFalseBoolsOmitted(t *testing.T) {
	m := paramsMap(t, &ScrapeConfig{URL: "https://u.test", PureCookies: false, SuperProxy: false})
	if len(m) != 1 {
		t.Errorf("false bools should be omitted, got %v", m)
	}
}

func TestRenderGating(t *testing.T) {
	_, err := (&ScrapeConfig{URL: "https://u.test", Screenshot: true}).toParams()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("expected render-gating ConfigError, got %v", err)
	}
	m := paramsMap(t, &ScrapeConfig{URL: "https://u.test", Render: true, ReturnJSON: true, Screenshot: true})
	if first(m, "render") != "true" || first(m, "screenShot") != "true" {
		t.Errorf("render+screenshot: %v", m)
	}
}

// render accepts only the documented true and false.
func TestRenderModeNewRejected(t *testing.T) {
	_, err := (&ScrapeConfig{URL: "https://u.test", RenderMode: "new"}).toParams()
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Errorf(`RenderMode "new": want ConfigError, got %v`, err)
	}
}

func TestPlayWithBrowserJSON(t *testing.T) {
	m := paramsMap(t, &ScrapeConfig{URL: "https://u.test", Render: true, PlayWithBrowser: []map[string]any{{"action": "click"}}})
	if first(m, "playWithBrowser") != `[{"action":"click"}]` {
		t.Errorf("playWithBrowser = %v", m["playWithBrowser"])
	}
}

func TestInvalidEnums(t *testing.T) {
	for _, c := range []*ScrapeConfig{
		{URL: "https://u.test", Device: "watch"},
		{URL: "https://u.test", Output: "pdf"},
		{URL: "https://u.test", Render: true, WaitUntil: "whenever"},
	} {
		if _, err := c.toParams(); err == nil {
			t.Errorf("expected error for %+v", c)
		}
	}
}

func TestExtraParams(t *testing.T) {
	m := paramsMap(t, &ScrapeConfig{URL: "https://u.test", ExtraParams: map[string]string{"brandNew": "x"}})
	if first(m, "brandNew") != "x" {
		t.Errorf("extraParams: %v", m)
	}
}

// JSON-valued params that cannot be encoded are a ConfigError before any
// request is made.
func TestUnserializableJSONParamsAreConfigErrors(t *testing.T) {
	for name, cfg := range map[string]*ScrapeConfig{
		"PlayWithBrowser": {URL: "https://u.test", Render: true, PlayWithBrowser: []map[string]any{{"c": make(chan int)}}},
	} {
		_, err := cfg.toParams()
		var ce *ConfigError
		if !errors.As(err, &ce) {
			t.Errorf("%s: want ConfigError, got %v", name, err)
		}
	}
	if boolStr(false) != "false" || boolStr(true) != "true" {
		t.Error("boolStr")
	}
}

// Enums fold ASCII case only, and an empty ExtraParams key is refused.
func TestEnumsFoldASCIIAndExtraKeysAreNonEmpty(t *testing.T) {
	var ce *ConfigError
	if _, err := (&ScrapeConfig{URL: "https://e.com", Device: "MOBİLE"}).toParams(); !errors.As(err, &ce) {
		t.Errorf("a dotted capital I matched mobile: %v", err)
	}
	if p, err := (&ScrapeConfig{URL: "https://e.com", Device: "MoBiLe"}).toParams(); err != nil || p[1] != (param{"device", "mobile"}) {
		t.Errorf("ASCII case folding: %v %v", p, err)
	}
	if _, err := (&ScrapeConfig{URL: "https://e.com", ExtraParams: map[string]string{"": "x"}}).toParams(); !errors.As(err, &ce) {
		t.Errorf("an empty ExtraParams key was accepted: %v", err)
	}
}
