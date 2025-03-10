package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Command line flags
	listenAddress = flag.String("web.listen-address", ":9733", "Address to listen on for web interface and telemetry")
	metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics")
	apiURL        = flag.String("lazylibrarian.api-url", "", "LazyLibrarian API URL")
	apiKey        = flag.String("lazylibrarian.api-key", "", "LazyLibrarian API key")
	scrapeTimeout = flag.Duration("scrape.timeout", 5*time.Second, "Timeout for API requests")
)

// LazyLibrarianExporter represents the exporter for LazyLibrarian
type LazyLibrarianExporter struct {
	apiURL        string
	apiKey        string
	client        *http.Client
	scrapeTimeout time.Duration

	// Metrics
	up                prometheus.Gauge
	totalScrapes      prometheus.Counter
	scrapeErrors      *prometheus.CounterVec
	scrapeDuration    prometheus.Gauge
	totalBooks        prometheus.Gauge
	totalAuthors      prometheus.Gauge
	totalSeries       prometheus.Gauge
	booksByStatus     *prometheus.GaugeVec
	activeJobs        prometheus.Gauge
	downloadsTotal    *prometheus.CounterVec
}

// NewLazyLibrarianExporter creates a new LazyLibrarian exporter
func NewLazyLibrarianExporter(apiURL, apiKey string, timeout time.Duration) *LazyLibrarianExporter {
	return &LazyLibrarianExporter{
		apiURL:        apiURL,
		apiKey:        apiKey,
		client:        &http.Client{Timeout: timeout},
		scrapeTimeout: timeout,

		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "up",
			Help:      "Was the last scrape of LazyLibrarian successful",
		}),

		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "lazylibrarian",
			Name:      "exporter_scrapes_total",
			Help:      "Current total LazyLibrarian scrapes",
		}),

		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "lazylibrarian",
			Name:      "exporter_scrape_errors_total",
			Help:      "Current total LazyLibrarian scrape errors",
		}, []string{"collector"}),

		scrapeDuration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "exporter_scrape_duration_seconds",
			Help:      "Duration of the scrape",
		}),

		totalBooks: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "books_total",
			Help:      "Total number of books in the library",
		}),

		totalAuthors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "authors_total",
			Help:      "Total number of authors in the library",
		}),

		totalSeries: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "series_total",
			Help:      "Total number of series in the library",
		}),

		booksByStatus: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "books_by_status",
			Help:      "Number of books by status",
		}, []string{"status"}),

		activeJobs: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "lazylibrarian",
			Name:      "active_jobs",
			Help:      "Number of active jobs",
		}),

		downloadsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "lazylibrarian",
			Name:      "downloads_total",
			Help:      "Total number of downloads",
		}, []string{"status"}),
	}
}

// Describe implements the prometheus.Collector interface
func (e *LazyLibrarianExporter) Describe(ch chan<- *prometheus.Desc) {
	e.up.Describe(ch)
	e.totalScrapes.Describe(ch)
	e.scrapeErrors.Describe(ch)
	e.scrapeDuration.Describe(ch)
	e.totalBooks.Describe(ch)
	e.totalAuthors.Describe(ch)
	e.totalSeries.Describe(ch)
	e.booksByStatus.Describe(ch)
	e.activeJobs.Describe(ch)
	e.downloadsTotal.Describe(ch)
}

// Collect implements the prometheus.Collector interface
func (e *LazyLibrarianExporter) Collect(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	
	scrapeStart := time.Now()
	defer func() {
		e.scrapeDuration.Set(time.Since(scrapeStart).Seconds())
		e.scrapeDuration.Collect(ch)
	}()

	if err := e.scrape(); err != nil {
		log.Printf("Error scraping LazyLibrarian: %s", err)
		e.up.Set(0)
	} else {
		e.up.Set(1)
	}

	e.up.Collect(ch)
	e.totalScrapes.Collect(ch)
	e.scrapeErrors.Collect(ch)
	e.totalBooks.Collect(ch)
	e.totalAuthors.Collect(ch)
	e.totalSeries.Collect(ch)
	e.booksByStatus.Collect(ch)
	e.activeJobs.Collect(ch)
	e.downloadsTotal.Collect(ch)
}

func (e *LazyLibrarianExporter) scrape() error {
	if err := e.collectServerInfo(); err != nil {
		e.scrapeErrors.WithLabelValues("server_info").Inc()
		return err
	}

	if err := e.collectLibraryStats(); err != nil {
		e.scrapeErrors.WithLabelValues("library_stats").Inc()
		return err
	}

	if err := e.collectJobsInfo(); err != nil {
		e.scrapeErrors.WithLabelValues("jobs_info").Inc()
		return err
	}

	return nil
}

// apiRequest makes a request to the LazyLibrarian API
func (e *LazyLibrarianExporter) apiRequest(cmd string, params map[string]string) (map[string]interface{}, error) {
	requestURL, err := url.Parse(e.apiURL)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL: %v", err)
	}

	query := requestURL.Query()
	query.Set("cmd", cmd)
	query.Set("apikey", e.apiKey)

	for key, value := range params {
		query.Set(key, value)
	}

	requestURL.RawQuery = query.Encode()

	req, err := http.NewRequest("GET", requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned non-200 status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %v", err)
	}

	return result, nil
}

func (e *LazyLibrarianExporter) collectServerInfo() error {
	data, err := e.apiRequest("getVersion", nil)
	if err != nil {
		return err
	}

	// Server info is collected but not exported as metrics
	// Could be exported as info metrics if needed
	log.Printf("Server info: version=%v, git_branch=%v", 
		data["version"], data["git_branch"])
	
	return nil
}

func (e *LazyLibrarianExporter) collectLibraryStats() error {
	// Get snatched books
	data, err := e.apiRequest("getSnatched", nil)
	if err != nil {
		return err
	}

	if snatched, ok := data["snatched"].([]interface{}); ok {
		e.downloadsTotal.WithLabelValues("snatched").Add(float64(len(snatched)))
	}

	// Get wanted books
	data, err = e.apiRequest("getWanted", nil)
	if err != nil {
		return err
	}

	if books, ok := data["books"].([]interface{}); ok {
		e.booksByStatus.WithLabelValues("wanted").Set(float64(len(books)))
	}

	// Get overall library stats
	data, err = e.apiRequest("getStats", nil)
	if err != nil {
		return err
	}

	if stats, ok := data["stats"].(map[string]interface{}); ok {
		// Set total counts
		if val, ok := stats["total_books"].(float64); ok {
			e.totalBooks.Set(val)
		}
		if val, ok := stats["total_authors"].(float64); ok {
			e.totalAuthors.Set(val)
		}
		if val, ok := stats["total_series"].(float64); ok {
			e.totalSeries.Set(val)
		}

		// Set status-specific counts
		statusMapping := map[string]string{
			"have":                "have",
			"read":                "read",
			"to_read":             "to_read",
			"skipped":             "skipped",
			"open_downloads":      "open",
			"processed_downloads": "processed",
			"failed_downloads":    "failed",
		}

		for apiKey, metricLabel := range statusMapping {
			if val, ok := stats[apiKey].(float64); ok {
				e.booksByStatus.WithLabelValues(metricLabel).Set(val)
			}
		}

		// Set download metrics
		if val, ok := stats["processed_downloads"].(float64); ok {
			e.downloadsTotal.WithLabelValues("processed").Add(val)
		}
		if val, ok := stats["failed_downloads"].(float64); ok {
			e.downloadsTotal.WithLabelValues("failed").Add(val)
		}
	}

	return nil
}

func (e *LazyLibrarianExporter) collectJobsInfo() error {
	data, err := e.apiRequest("getJobs", nil)
	if err != nil {
		return err
	}

	if jobs, ok := data["jobs"].([]interface{}); ok {
		e.activeJobs.Set(float64(len(jobs)))
	}

	return nil
}

func main() {
	flag.Parse()

	if *apiURL == "" {
		log.Fatal("LazyLibrarian API URL is required")
	}

	if *apiKey == "" {
		log.Fatal("LazyLibrarian API key is required")
	}

	exporter := NewLazyLibrarianExporter(*apiURL, *apiKey, *scrapeTimeout)
	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>LazyLibrarian Exporter</title></head>
			<body>
			<h1>LazyLibrarian Exporter</h1>
			<p><a href="` + *metricsPath + `">Metrics</a></p>
			</body>
			</html>`))
	})

	log.Printf("Starting LazyLibrarian exporter on %s", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
