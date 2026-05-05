package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

// ScrapeRequest represents the incoming request body
type ScrapeRequest struct {
	URL     string `json:"url"`
	Format  string `json:"format"`  // "md" or "json"
	Country string `json:"country"` // optional Webshare proxy country code, for example "us" or "gb"
}

// ScrapeResponse represents the response data
type ScrapeResponse struct {
	URL         string            `json:"url"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Content     string            `json:"content"`
	Format      string            `json:"format"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Links       []string          `json:"links,omitempty"`
	Images      []string          `json:"images,omitempty"`
	Country     string            `json:"country,omitempty"`
	ScrapedAt   time.Time         `json:"scraped_at"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

const webshareProxyCacheTTL = 5 * time.Minute

var metricsState = newMetricsState()

type webshareProxy struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	ProxyAddress     string `json:"proxy_address"`
	Port             int    `json:"port"`
	Valid            bool   `json:"valid"`
	CountryCode      string `json:"country_code"`
	CityName         string `json:"city_name"`
	LastVerification string `json:"last_verification"`
}

type webshareProxyListResponse struct {
	Count   int             `json:"count"`
	Next    string          `json:"next"`
	Results []webshareProxy `json:"results"`
}

var webshareCache = struct {
	sync.Mutex
	proxies   []webshareProxy
	fetchedAt time.Time
}{}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.Handle("/scrape", instrumentHandler("/scrape", http.HandlerFunc(handleScrape)))
	http.Handle("/health", instrumentHandler("/health", http.HandlerFunc(handleHealth)))
	http.HandleFunc("/ready", handleHealth)
	http.HandleFunc("/metrics", handleMetrics)

	log.Printf("scraper listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

type metricsStateData struct {
	startedAt        time.Time
	inFlight         atomic.Int64
	proxyErrorsTotal atomic.Int64
	mu               sync.Mutex
	httpRequests     map[string]int64
	httpErrors       map[string]int64
	httpDuration     map[string]*histogram
	scraperJobs      map[string]int64
	scraperDuration  map[string]*histogram
}

type histogram struct {
	buckets []float64
	counts  []int64
	sum     float64
	count   int64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func newMetricsState() *metricsStateData {
	return &metricsStateData{
		startedAt:       time.Now(),
		httpRequests:    map[string]int64{},
		httpErrors:      map[string]int64{},
		httpDuration:    map[string]*histogram{},
		scraperJobs:     map[string]int64{},
		scraperDuration: map[string]*histogram{},
	}
}

func newHistogram() *histogram {
	buckets := []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
	return &histogram{buckets: buckets, counts: make([]int64, len(buckets))}
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func instrumentHandler(path string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		metricsState.inFlight.Add(1)
		defer metricsState.inFlight.Add(-1)

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		metricsState.recordHTTP(r.Method, path, recorder.status, time.Since(start).Seconds())
	})
}

func (m *metricsStateData) recordHTTP(method string, path string, status int, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	statusCode := fmt.Sprintf("%d", status)
	key := method + "|" + path + "|" + statusCode
	m.httpRequests[key]++
	if status >= 500 {
		m.httpErrors[key]++
	}
	histogramKey := method + "|" + path
	m.histogram(m.httpDuration, histogramKey).observe(seconds)
}

func (m *metricsStateData) recordScrape(result string, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scraperJobs[result]++
	m.histogram(m.scraperDuration, result).observe(seconds)
}

func (m *metricsStateData) histogram(histograms map[string]*histogram, key string) *histogram {
	h, ok := histograms[key]
	if !ok {
		h = newHistogram()
		histograms[key] = h
	}
	return h
}

func (h *histogram) observe(seconds float64) {
	h.count++
	h.sum += seconds
	for i, bucket := range h.buckets {
		if seconds <= bucket {
			h.counts[i]++
		}
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metricsState.writePrometheus(w)
}

func (m *metricsStateData) writePrometheus(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var runtimeStats runtime.MemStats
	runtime.ReadMemStats(&runtimeStats)

	fmt.Fprintf(w, "# HELP http_requests_total Total HTTP requests by method, path, and status.\n")
	fmt.Fprintf(w, "# TYPE http_requests_total counter\n")
	for key, count := range m.httpRequests {
		method, path, status := splitMetricKey(key)
		fmt.Fprintf(w, "http_requests_total{service=\"scraper\",method=\"%s\",path=\"%s\",status=\"%s\"} %d\n", method, path, status, count)
	}

	fmt.Fprintf(w, "# HELP http_errors_total Total HTTP 5xx responses by method, path, and status.\n")
	fmt.Fprintf(w, "# TYPE http_errors_total counter\n")
	for key, count := range m.httpErrors {
		method, path, status := splitMetricKey(key)
		fmt.Fprintf(w, "http_errors_total{service=\"scraper\",method=\"%s\",path=\"%s\",status=\"%s\"} %d\n", method, path, status, count)
	}

	fmt.Fprintf(w, "# HELP http_requests_in_flight Current in-flight HTTP requests.\n")
	fmt.Fprintf(w, "# TYPE http_requests_in_flight gauge\n")
	fmt.Fprintf(w, "http_requests_in_flight{service=\"scraper\"} %d\n", m.inFlight.Load())

	fmt.Fprintf(w, "# HELP http_request_duration_seconds HTTP request duration by method and path.\n")
	fmt.Fprintf(w, "# TYPE http_request_duration_seconds histogram\n")
	for key, histogram := range m.httpDuration {
		method, path, _ := splitMetricKey(key)
		writeHistogram(w, "http_request_duration_seconds", fmt.Sprintf("service=\"scraper\",method=\"%s\",path=\"%s\"", method, path), histogram)
	}

	fmt.Fprintf(w, "# HELP scraper_jobs_total Total scrape jobs by result.\n")
	fmt.Fprintf(w, "# TYPE scraper_jobs_total counter\n")
	for result, count := range m.scraperJobs {
		fmt.Fprintf(w, "scraper_jobs_total{result=\"%s\"} %d\n", result, count)
	}

	fmt.Fprintf(w, "# HELP scraper_job_duration_seconds Scrape job duration by result.\n")
	fmt.Fprintf(w, "# TYPE scraper_job_duration_seconds histogram\n")
	for result, histogram := range m.scraperDuration {
		writeHistogram(w, "scraper_job_duration_seconds", fmt.Sprintf("result=\"%s\"", result), histogram)
	}

	fmt.Fprintf(w, "# HELP scraper_proxy_errors_total Total Webshare proxy selection/configuration errors.\n")
	fmt.Fprintf(w, "# TYPE scraper_proxy_errors_total counter\n")
	fmt.Fprintf(w, "scraper_proxy_errors_total %d\n", m.proxyErrorsTotal.Load())
	fmt.Fprintf(w, "# HELP process_start_time_seconds Start time of the process since unix epoch in seconds.\n")
	fmt.Fprintf(w, "# TYPE process_start_time_seconds gauge\n")
	fmt.Fprintf(w, "process_start_time_seconds %.0f\n", float64(m.startedAt.Unix()))
	fmt.Fprintf(w, "# HELP go_goroutines Number of goroutines that currently exist.\n")
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\n")
	fmt.Fprintf(w, "go_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP go_memstats_alloc_bytes Number of bytes allocated and still in use.\n")
	fmt.Fprintf(w, "# TYPE go_memstats_alloc_bytes gauge\n")
	fmt.Fprintf(w, "go_memstats_alloc_bytes %d\n", runtimeStats.Alloc)
}

func writeHistogram(w io.Writer, name string, labels string, histogram *histogram) {
	for i, bucket := range histogram.buckets {
		fmt.Fprintf(w, "%s_bucket{%s,le=\"%g\"} %d\n", name, labels, bucket, histogram.counts[i])
	}
	fmt.Fprintf(w, "%s_bucket{%s,le=\"+Inf\"} %d\n", name, labels, histogram.count)
	fmt.Fprintf(w, "%s_sum{%s} %.6f\n", name, labels, histogram.sum)
	fmt.Fprintf(w, "%s_count{%s} %d\n", name, labels, histogram.count)
}

func splitMetricKey(key string) (string, string, string) {
	parts := strings.Split(key, "|")
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func handleScrape(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	result := "success"
	defer func() {
		metricsState.recordScrape(result, time.Since(start).Seconds())
	}()

	if r.Method != http.MethodPost {
		result = "error"
		respondWithError(w, http.StatusMethodNotAllowed, "Method not allowed", "Only POST requests are supported")
		return
	}

	var req ScrapeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		result = "error"
		respondWithError(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	if req.URL == "" {
		result = "error"
		respondWithError(w, http.StatusBadRequest, "Missing URL", "url field is required")
		return
	}

	format := strings.ToLower(req.Format)
	if format == "" {
		format = "json"
	}
	if format != "md" && format != "json" {
		result = "error"
		respondWithError(w, http.StatusBadRequest, "Invalid format", "format must be 'md' or 'json'")
		return
	}

	country := normalizeCountryCode(req.Country)
	if err := validateCountry(country); err != nil {
		result = "error"
		respondWithError(w, http.StatusBadRequest, "Invalid country", err.Error())
		return
	}

	req.URL = normalizeURL(req.URL)
	scrapeResult, err := scrapeURL(req.URL, format, country)
	if err != nil {
		result = "error"
		respondWithError(w, http.StatusInternalServerError, "Scraping failed", err.Error())
		return
	}

	if format == "md" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, scrapeResult.Content)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scrapeResult)
	}
}

func scrapeURL(url, format string, country string) (*ScrapeResponse, error) {
	if country != "" {
		proxy, err := selectWebshareProxy(country)
		if err != nil {
			metricsState.proxyErrorsTotal.Add(1)
			return nil, err
		}
		transport, err := webshareProxyTransport(proxy)
		if err != nil {
			metricsState.proxyErrorsTotal.Add(1)
			return nil, err
		}
		return scrapeURLWithTransport(url, format, country, transport)
	}

	return scrapeURLWithTransport(url, format, country, nil)
}

func scrapeURLWithTransport(url, format string, country string, transport *http.Transport) (*ScrapeResponse, error) {
	var htmlContent string
	var title string
	var description string
	var links []string
	var images []string
	metadata := make(map[string]string)

	c := colly.NewCollector(
		colly.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"),
	)
	c.SetRequestTimeout(30 * time.Second)

	if transport != nil {
		c.WithTransport(transport)
	}

	c.OnResponse(func(r *colly.Response) {
		htmlContent = string(r.Body)
	})

	c.OnHTML("title", func(e *colly.HTMLElement) {
		title = strings.TrimSpace(e.Text)
	})

	c.OnHTML("meta[name='description']", func(e *colly.HTMLElement) {
		description = e.Attr("content")
	})

	c.OnHTML("meta[property='og:title']", func(e *colly.HTMLElement) {
		metadata["og_title"] = e.Attr("content")
	})

	c.OnHTML("meta[property='og:description']", func(e *colly.HTMLElement) {
		metadata["og_description"] = e.Attr("content")
	})

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		link := e.Attr("href")
		if link != "" && !strings.HasPrefix(link, "#") {
			links = append(links, link)
		}
	})

	c.OnHTML("img[src]", func(e *colly.HTMLElement) {
		img := e.Attr("src")
		if img != "" {
			images = append(images, img)
		}
	})

	if err := c.Visit(url); err != nil {
		return nil, fmt.Errorf("failed to visit URL: %w", err)
	}

	var content string
	if format == "md" {
		content = convertToMarkdown(htmlContent, title, description)
	} else {
		// For JSON, extract main content text
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
		if err != nil {
			return nil, fmt.Errorf("failed to parse HTML: %w", err)
		}
		// Remove script and style elements
		doc.Find("script, style, nav, header, footer, aside").Remove()
		content = strings.TrimSpace(doc.Find("body").Text())
		// Clean up whitespace
		content = strings.Join(strings.Fields(content), " ")
	}

	return &ScrapeResponse{
		URL:         url,
		Title:       title,
		Description: description,
		Content:     content,
		Format:      format,
		Metadata:    metadata,
		Links:       deduplicate(links),
		Images:      deduplicate(images),
		Country:     country,
		ScrapedAt:   time.Now().UTC(),
	}, nil
}

func normalizeURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}

	// Check if it's a domain-like string (no spaces, has a dot)
	if strings.Contains(rawURL, ".") && !strings.Contains(rawURL, " ") {
		return "https://" + rawURL
	}

	// Otherwise, treat as a path or relative URL
	return "https://" + rawURL
}

func validateCountry(country string) error {
	if country == "" {
		return nil
	}
	if len(country) != 2 || country[0] < 'a' || country[0] > 'z' || country[1] < 'a' || country[1] > 'z' {
		return fmt.Errorf("country must be a two-letter country code, for example us, gb, nl, pl")
	}
	return nil
}

func normalizeCountryCode(country string) string {
	country = strings.ToLower(strings.TrimSpace(country))
	if country == "uk" {
		return "gb"
	}
	return country
}

func webshareProxyTransport(proxy webshareProxy) (*http.Transport, error) {
	username := proxy.Username
	password := proxy.Password
	if username == "" {
		username = os.Getenv("WEBSHARE_PROXY_USERNAME")
	}
	if password == "" {
		password = os.Getenv("WEBSHARE_PROXY_PASSWORD")
	}
	if username == "" || password == "" {
		return nil, fmt.Errorf("Webshare proxy credentials are missing for selected proxy")
	}

	proxyURL := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", proxy.ProxyAddress, proxy.Port),
		User:   url.UserPassword(username, password),
	}
	if _, err := url.Parse(proxyURL.String()); err != nil {
		return nil, fmt.Errorf("failed to configure Webshare proxy: %w", err)
	}

	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}, nil
}

func selectWebshareProxy(country string) (webshareProxy, error) {
	proxies, err := getWebshareProxies(false)
	if err != nil {
		return webshareProxy{}, err
	}
	if proxy, ok := findWebshareProxy(proxies, country); ok {
		return proxy, nil
	}

	proxies, err = getWebshareProxies(true)
	if err != nil {
		return webshareProxy{}, err
	}
	if proxy, ok := findWebshareProxy(proxies, country); ok {
		return proxy, nil
	}

	return webshareProxy{}, fmt.Errorf("no valid Webshare proxy available for country %q; available countries: %s", country, strings.Join(availableWebshareCountries(proxies), ", "))
}

func findWebshareProxy(proxies []webshareProxy, country string) (webshareProxy, bool) {
	for _, proxy := range proxies {
		if proxy.Valid && strings.EqualFold(proxy.CountryCode, country) {
			return proxy, true
		}
	}
	return webshareProxy{}, false
}

func getWebshareProxies(forceRefresh bool) ([]webshareProxy, error) {
	webshareCache.Lock()
	defer webshareCache.Unlock()

	if !forceRefresh && len(webshareCache.proxies) > 0 && time.Since(webshareCache.fetchedAt) < webshareProxyCacheTTL {
		return webshareCache.proxies, nil
	}

	proxies, err := fetchWebshareProxies()
	if err != nil {
		return nil, err
	}
	webshareCache.proxies = proxies
	webshareCache.fetchedAt = time.Now()
	return proxies, nil
}

func fetchWebshareProxies() ([]webshareProxy, error) {
	apiKey := os.Getenv("WEBSHARE_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("WEBSHARE_API_KEY is required when country is set")
	}

	var proxies []webshareProxy
	listURL := "https://proxy.webshare.io/api/v2/proxy/list/?mode=direct&page=1&page_size=100"
	client := &http.Client{Timeout: 20 * time.Second}

	for listURL != "" {
		req, err := http.NewRequest(http.MethodGet, listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Webshare request: %w", err)
		}
		req.Header.Set("Authorization", "Token "+apiKey)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch Webshare proxies: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("failed to read Webshare response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("Webshare proxy list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var result webshareProxyListResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("failed to parse Webshare proxy list: %w", err)
		}
		proxies = append(proxies, result.Results...)
		listURL = result.Next
	}

	return proxies, nil
}

func availableWebshareCountries(proxies []webshareProxy) []string {
	seen := make(map[string]bool)
	var countries []string
	for _, proxy := range proxies {
		if !proxy.Valid || proxy.CountryCode == "" {
			continue
		}
		country := strings.ToLower(proxy.CountryCode)
		if seen[country] {
			continue
		}
		seen[country] = true
		countries = append(countries, country)
	}
	if len(countries) == 0 {
		return []string{"none"}
	}
	sort.Strings(countries)
	return countries
}

func convertToMarkdown(html string, title string, description string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}

	// Remove script/style only - keep semantic structure
	doc.Find("script, style").Remove()

	var md strings.Builder

	// Add title as H1 if present
	if title != "" {
		md.WriteString("# ")
		md.WriteString(title)
		md.WriteString("\n\n")
	}

	// Add description if present
	if description != "" {
		md.WriteString(description)
		md.WriteString("\n\n")
	}

	// Process the body
	doc.Find("body").Each(func(i int, s *goquery.Selection) {
		processNode(&md, s)
	})

	return cleanMarkdown(md.String())
}

func processNode(md *strings.Builder, s *goquery.Selection) {
	s.Children().Each(func(i int, child *goquery.Selection) {
		nodeType := goquery.NodeName(child)

		// Add spacing before block elements (except first child)
		isBlock := isBlockElement(nodeType)
		if isBlock && i > 0 {
			md.WriteString("\n")
		}

		switch nodeType {
		case "h1":
			md.WriteString("\n# ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "h2":
			md.WriteString("\n## ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "h3":
			md.WriteString("\n### ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "h4":
			md.WriteString("\n#### ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "h5":
			md.WriteString("\n##### ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "h6":
			md.WriteString("\n###### ")
			md.WriteString(cleanInlineText(child.Text()))
			md.WriteString("\n")
		case "p":
			md.WriteString("\n")
			processInline(md, child)
			md.WriteString("\n")
		case "div":
			// Check if div contains only inline content
			if isInlineContainer(child) {
				processInline(md, child)
				md.WriteString("\n")
			} else {
				processNode(md, child)
			}
		case "section", "article", "main":
			processNode(md, child)
		case "ul", "ol":
			md.WriteString("\n")
			child.Children().Each(func(j int, li *goquery.Selection) {
				if goquery.NodeName(li) == "li" {
					md.WriteString("- ")
					processInline(md, li)
					md.WriteString("\n")
				}
			})
		case "br":
			md.WriteString("\n")
		case "hr":
			md.WriteString("\n---\n")
		case "blockquote":
			md.WriteString("\n> ")
			processInline(md, child)
			md.WriteString("\n")
		case "pre":
			md.WriteString("\n```\n")
			md.WriteString(strings.TrimSpace(child.Text()))
			md.WriteString("\n```\n")
		case "img":
			src, _ := child.Attr("src")
			alt, _ := child.Attr("alt")
			if src != "" {
				md.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
			}
		case "a":
			href, _ := child.Attr("href")
			text := strings.TrimSpace(child.Text())
			if href != "" && text != "" {
				md.WriteString(fmt.Sprintf("[%s](%s)", text, href))
			}
		case "nav", "header", "footer":
			// Process but add visual separation
			processNode(md, child)
		default:
			// For inline elements, process their children
			if isInlineElement(nodeType) {
				processInline(md, child)
			} else {
				processNode(md, child)
			}
		}
	})
}

func isBlockElement(tag string) bool {
	blockTags := map[string]bool{
		"div": true, "p": true, "h1": true, "h2": true, "h3": true,
		"h4": true, "h5": true, "h6": true, "ul": true, "ol": true,
		"li": true, "blockquote": true, "pre": true, "hr": true,
		"section": true, "article": true, "main": true, "nav": true,
		"header": true, "footer": true, "aside": true,
	}
	return blockTags[tag]
}

func isInlineContainer(s *goquery.Selection) bool {
	isInline := true
	s.Children().Each(func(i int, child *goquery.Selection) {
		nodeType := goquery.NodeName(child)
		if isBlockElement(nodeType) {
			isInline = false
		}
	})
	return isInline
}

func processInline(md *strings.Builder, s *goquery.Selection) {
	s.Contents().Each(func(i int, child *goquery.Selection) {
		if goquery.NodeName(child) == "#text" {
			text := strings.TrimSpace(child.Text())
			if text != "" {
				md.WriteString(text)
				md.WriteString(" ")
			}
		} else {
			nodeType := goquery.NodeName(child)
			switch nodeType {
			case "a":
				href, _ := child.Attr("href")
				text := strings.TrimSpace(child.Text())
				if href != "" && text != "" {
					writeSpaceIfNeeded(md)
					md.WriteString(fmt.Sprintf("[%s](%s)", cleanInlineText(text), href))
					md.WriteString(" ")
				}
			case "img":
				src, _ := child.Attr("src")
				alt, _ := child.Attr("alt")
				if src != "" {
					md.WriteString(fmt.Sprintf("![%s](%s) ", alt, src))
				}
			case "strong", "b":
				writeSpaceIfNeeded(md)
				md.WriteString("**")
				md.WriteString(cleanInlineText(child.Text()))
				md.WriteString("** ")
			case "em", "i":
				writeSpaceIfNeeded(md)
				md.WriteString("*")
				md.WriteString(cleanInlineText(child.Text()))
				md.WriteString("* ")
			case "code":
				md.WriteString("`")
				md.WriteString(strings.TrimSpace(child.Text()))
				md.WriteString("` ")
			case "br":
				md.WriteString("\n")
			default:
				processInline(md, child)
			}
		}
	})
}

func writeSpaceIfNeeded(md *strings.Builder) {
	value := md.String()
	if value == "" {
		return
	}
	last := value[len(value)-1]
	if last != ' ' && last != '\n' {
		md.WriteString(" ")
	}
}

func cleanInlineText(input string) string {
	return strings.Join(strings.Fields(input), " ")
}

func isInlineElement(tag string) bool {
	inlineTags := map[string]bool{
		"span": true, "em": true, "i": true, "strong": true, "b": true,
		"a": true, "code": true, "small": true, "sub": true, "sup": true,
		"mark": true, "del": true, "ins": true, "abbr": true, "time": true,
	}
	return inlineTags[tag]
}

func cleanMarkdown(input string) string {
	// Remove email obfuscation links
	input = removeEmailObfuscation(input)
	input = strings.ReplaceAll(input, "\u00a0", " ")
	input = strings.ReplaceAll(input, "[email protected]", "email protected")
	input = strings.ReplaceAll(input, "email protected", "[email protected]")
	input = strings.ReplaceAll(input, "[email protected]]", "[email protected]")
	input = strings.ReplaceAll(input, "[email protected]", "")
	input = strings.ReplaceAll(input, "](", "](")
	input = strings.ReplaceAll(input, ")[", ") [")
	input = strings.ReplaceAll(input, ")![", ") ![")
	input = strings.ReplaceAll(input, " .", ".")
	input = splitDenseLinkLines(input)
	input = separateSectionLabels(input)

	// Fix multiple consecutive blank lines
	for strings.Contains(input, "\n\n\n") {
		input = strings.ReplaceAll(input, "\n\n\n", "\n\n")
	}

	// Clean up extra spaces around markdown
	input = strings.ReplaceAll(input, "  ", " ")
	input = strings.ReplaceAll(input, "  ", " ")

	// Trim each line
	lines := strings.Split(input, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	input = strings.Join(lines, "\n")

	return strings.TrimSpace(input)
}

func splitDenseLinkLines(input string) string {
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		if strings.Count(line, "](") > 1 && len(line) > 100 {
			line = strings.ReplaceAll(line, ") [", ")\n[")
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func separateSectionLabels(input string) string {
	labels := []string{"Navigation", "More", "Legal", "What I Offer", "Selected Work", "Why Staticfish", "Next Step"}
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, label := range labels {
			if trimmed == label {
				lines[i] = "## " + label
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

func removeEmailObfuscation(input string) string {
	// Match [[anything]](/cdn-cgi/l/email-protection) and replace with just the inner text
	var result strings.Builder
	i := 0
	for i < len(input) {
		if i < len(input)-1 && input[i] == '[' && input[i+1] == '[' {
			// Find ]( after the opening [[
			end := strings.Index(input[i:], "](")
			if end != -1 {
				// Check if it's the email protection URL
				urlStart := i + end + 2
				if strings.HasPrefix(input[urlStart:], "/cdn-cgi/l/email-protection") {
					// Extract text between [[ and ]]
					emailStart := i + 2
					emailEnd := i + end
					if emailEnd > emailStart {
						result.WriteString(input[emailStart:emailEnd])
						// Skip past the closing )
						closeParen := strings.Index(input[i+end:], ")")
						if closeParen != -1 {
							i = i + end + closeParen + 1
							continue
						}
					}
				}
			}
		}
		result.WriteByte(input[i])
		i++
	}
	return result.String()
}

func deduplicate(slice []string) []string {
	seen := make(map[string]bool)
	result := []string{}
	for _, s := range slice {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

func respondWithError(w http.ResponseWriter, status int, err string, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Error:   err,
		Details: details,
	})
}
