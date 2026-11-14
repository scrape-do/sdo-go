// SPDX-License-Identifier: Apache-2.0

// Package sdo is the official Go SDK for the Scrape.do web scraping API.
//
// A [Client] sends a scrape request through the Scrape.do gateway, decompresses
// the response and returns it as a [Result]. Failures are typed errors
// ([APIError], [TransportError], [ConfigError], [ResponseTooLargeError]) to
// match with errors.As. The same client calls Scrape.do plugins
// ([Client.Plugin]), reads usage statistics ([Client.Usage]) and drives the
// asynchronous job API ([Client.Jobs]). The only credential is the API token,
// given to [New] or read from SDO_TOKEN; the SDK sends no telemetry.
//
// # Quickstart
//
//	client, err := sdo.New("YOUR_TOKEN") // or "" to read SDO_TOKEN
//	if err != nil {
//		log.Fatal(err)
//	}
//	res, err := client.Scrape(context.Background(), &sdo.ScrapeConfig{
//		URL:    "https://example.com",
//		Render: true,
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(res.StatusCode, res.Text())
//
// The request parameters are documented at https://scrape.do/documentation;
// the README at https://github.com/scrape-do/sdo-go#readme covers retries,
// timeouts, plugins and async jobs.
package sdo
