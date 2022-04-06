// Package scraper is a simple client to scrape and parse prometheus metrics.
// Intended for testing.
package scraper

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// Scraper scrapes metrics from a HTTP endpoint
type Scraper struct {
	Client   *http.Client
	Retries  int
	Interval time.Duration
}

func New() *Scraper {
	return &Scraper{
		Client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
		},
		Retries:  10,
		Interval: time.Second,
	}
}

// Scrape the url, return the parsed metrics.
func (s *Scraper) Scrape(url string) (map[string]*dto.MetricFamily, error) {
	resp, err := s.Client.Get(url)
	for i := 0; i < s.Retries && err != nil; i++ {
		time.Sleep(s.Interval)
		resp, err = s.Client.Get(url)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("scrape error: %v: %v", resp.Status, url)
	}
	if resp.Body == nil {
		return nil, fmt.Errorf("scrape error: response has no body: %v", url)
	}
	parser := expfmt.TextParser{}
	return parser.TextToMetricFamilies(resp.Body)
}

func FindMetric(mf *dto.MetricFamily, label, value string) *dto.Metric {
	for _, m := range mf.Metric {
		for _, lp := range m.Label {
			if *lp.Name == label && *lp.Value == value {
				return m
			}
		}
	}
	return nil
}

func Labels(m *dto.Metric) map[string]string {
	labels := map[string]string{}
	for _, kv := range m.Label {
		labels[kv.GetName()] = kv.GetValue()
	}
	return labels
}
