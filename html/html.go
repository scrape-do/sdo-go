// SPDX-License-Identifier: Apache-2.0

// Package html parses a scrape result as an HTML document for CSS selection
// with goquery. It lives apart from package sdo so a program that never
// parses HTML does not link goquery and its dependencies.
package html

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/scrape-do/sdo-go"
)

// Document parses the result's text (decoded per its charset, see
// sdo.Result.Text) as an HTML document.
func Document(r *sdo.Result) (*goquery.Document, error) {
	if r == nil {
		return nil, &sdo.ConfigError{Message: "html.Document needs a *sdo.Result, got nil"}
	}
	return goquery.NewDocumentFromReader(strings.NewReader(r.Text()))
}
