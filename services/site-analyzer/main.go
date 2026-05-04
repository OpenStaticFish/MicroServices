package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type AnalyzeRequest struct {
	URL string `json:"url"`
}

type AnalyzeResponse struct {
	URL          string         `json:"url"`
	FinalURL     string         `json:"final_url,omitempty"`
	Domain       string         `json:"domain"`
	AnalyzedAt   time.Time      `json:"analyzed_at"`
	Hosting      HostingInfo    `json:"hosting"`
	DNS          DNSInfo        `json:"dns"`
	Technologies TechnologyInfo `json:"technologies"`
	Security     SecurityInfo   `json:"security"`
	HTTP         HTTPInfo       `json:"http"`
	Warnings     []string       `json:"warnings,omitempty"`
}

type HostingInfo struct {
	Provider       string   `json:"provider,omitempty"`
	CDN            string   `json:"cdn,omitempty"`
	OriginProvider string   `json:"origin_provider,omitempty"`
	OriginEvidence []string `json:"origin_evidence,omitempty"`
	IPAddresses    []string `json:"ip_addresses,omitempty"`
	ReverseDNS     []string `json:"reverse_dns,omitempty"`
}

type DNSInfo struct {
	Provider    string              `json:"provider,omitempty"`
	Nameservers []string            `json:"nameservers,omitempty"`
	Records     map[string][]string `json:"records,omitempty"`
}

type TechnologyInfo struct {
	CMS                 string   `json:"cms,omitempty"`
	Framework           string   `json:"framework,omitempty"`
	Server              string   `json:"server,omitempty"`
	ProgrammingLanguage string   `json:"programming_language,omitempty"`
	Detected            []string `json:"detected,omitempty"`
}

type SecurityInfo struct {
	SSL     bool              `json:"ssl"`
	HSTS    bool              `json:"hsts"`
	Headers map[string]string `json:"headers,omitempty"`
}

type HTTPInfo struct {
	StatusCode int               `json:"status_code,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

type fetchedSite struct {
	FinalURL   string
	StatusCode int
	Header     http.Header
	Body       []byte
	TLS        bool
}

type config struct {
	port                  string
	maxConcurrentAnalyses int
	analysisTimeout       time.Duration
	fetchTimeout          time.Duration
	maxRequestBytes       int64
	maxResponseBytes      int64
	readHeaderTimeout     time.Duration
	readTimeout           time.Duration
	writeTimeout          time.Duration
	idleTimeout           time.Duration
	shutdownTimeout       time.Duration
	maxIdleConns          int
	maxIdleConnsPerHost   int
	responseHeaderTimeout time.Duration
	tlsHandshakeTimeout   time.Duration
	expectContinueTimeout time.Duration
	idleConnTimeout       time.Duration
}

type app struct {
	config    config
	client    *http.Client
	semaphore chan struct{}
	metrics   *metrics
}

type metrics struct {
	requestsTotal        atomic.Int64
	analyzeRequestsTotal atomic.Int64
	analyzeRejectedTotal atomic.Int64
	analyzeErrorsTotal   atomic.Int64
	analyzeDurationCount atomic.Int64
	analyzeDurationNanos atomic.Int64
	activeAnalyses       atomic.Int64
}

var errUnsafeTarget = errors.New("target resolves to a private or otherwise unsafe address")

func main() {
	cfg := loadConfig()
	application := newApp(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/analyze", application.handleAnalyze)
	mux.HandleFunc("/health", application.handleHealth)
	mux.HandleFunc("/ready", application.handleReady)
	mux.HandleFunc("/metrics", application.handleMetrics)

	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           mux,
		ReadHeaderTimeout: cfg.readHeaderTimeout,
		ReadTimeout:       cfg.readTimeout,
		WriteTimeout:      cfg.writeTimeout,
		IdleTimeout:       cfg.idleTimeout,
		MaxHeaderBytes:    16 * 1024,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("site-analyzer listening on :%s max_concurrent_analyses=%d analysis_timeout=%s", cfg.port, cfg.maxConcurrentAnalyses, cfg.analysisTimeout)
		serverErrors <- server.ListenAndServe()
	}()

	shutdownSignals := make(chan os.Signal, 1)
	signal.Notify(shutdownSignals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case signal := <-shutdownSignals:
		log.Printf("received %s, shutting down", signal)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
			if closeErr := server.Close(); closeErr != nil {
				log.Printf("forced shutdown failed: %v", closeErr)
			}
		}
	}
}

func newApp(cfg config) *app {
	application := &app{
		config:    cfg,
		semaphore: make(chan struct{}, cfg.maxConcurrentAnalyses),
		metrics:   &metrics{},
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           application.safeDialContext,
		MaxIdleConns:          cfg.maxIdleConns,
		MaxIdleConnsPerHost:   cfg.maxIdleConnsPerHost,
		IdleConnTimeout:       cfg.idleConnTimeout,
		TLSHandshakeTimeout:   cfg.tlsHandshakeTimeout,
		ResponseHeaderTimeout: cfg.responseHeaderTimeout,
		ExpectContinueTimeout: cfg.expectContinueTimeout,
	}
	application.client = &http.Client{
		Timeout:   cfg.fetchTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("stopped after 5 redirects")
			}
			return validateTargetURL(req.URL)
		},
	}

	return application
}

func loadConfig() config {
	analysisTimeout := durationEnv("ANALYSIS_TIMEOUT", 15*time.Second)
	fetchTimeout := durationEnv("FETCH_TIMEOUT", 10*time.Second)
	writeTimeout := durationEnv("WRITE_TIMEOUT", analysisTimeout+5*time.Second)
	if writeTimeout <= analysisTimeout {
		writeTimeout = analysisTimeout + 5*time.Second
	}

	return config{
		port:                  stringEnv("PORT", "8090"),
		maxConcurrentAnalyses: intEnv("MAX_CONCURRENT_ANALYSES", 20),
		analysisTimeout:       analysisTimeout,
		fetchTimeout:          fetchTimeout,
		maxRequestBytes:       int64Env("MAX_REQUEST_BYTES", 4*1024),
		maxResponseBytes:      int64Env("MAX_RESPONSE_BYTES", 2*1024*1024),
		readHeaderTimeout:     durationEnv("READ_HEADER_TIMEOUT", 2*time.Second),
		readTimeout:           durationEnv("READ_TIMEOUT", 5*time.Second),
		writeTimeout:          writeTimeout,
		idleTimeout:           durationEnv("IDLE_TIMEOUT", 60*time.Second),
		shutdownTimeout:       durationEnv("SHUTDOWN_TIMEOUT", 25*time.Second),
		maxIdleConns:          intEnv("MAX_IDLE_CONNS", 200),
		maxIdleConnsPerHost:   intEnv("MAX_IDLE_CONNS_PER_HOST", 20),
		responseHeaderTimeout: durationEnv("RESPONSE_HEADER_TIMEOUT", 5*time.Second),
		tlsHandshakeTimeout:   durationEnv("TLS_HANDSHAKE_TIMEOUT", 5*time.Second),
		expectContinueTimeout: durationEnv("EXPECT_CONTINUE_TIMEOUT", time.Second),
		idleConnTimeout:       durationEnv("IDLE_CONN_TIMEOUT", 90*time.Second),
	}
}

func stringEnv(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func int64Env(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}

func (a *app) handleHealth(w http.ResponseWriter, r *http.Request) {
	a.metrics.requestsTotal.Add(1)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func (a *app) handleReady(w http.ResponseWriter, r *http.Request) {
	a.metrics.requestsTotal.Add(1)
	if int(a.metrics.activeAnalyses.Load()) >= a.config.maxConcurrentAnalyses {
		respondWithError(w, http.StatusServiceUnavailable, "Service saturated", "maximum concurrent analyses are already running")
		return
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ready")
}

func (a *app) handleMetrics(w http.ResponseWriter, r *http.Request) {
	a.metrics.requestsTotal.Add(1)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	durationSeconds := float64(a.metrics.analyzeDurationNanos.Load()) / float64(time.Second)
	fmt.Fprintf(w, "# HELP site_analyzer_requests_total Total HTTP requests received.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_requests_total counter\n")
	fmt.Fprintf(w, "site_analyzer_requests_total %d\n", a.metrics.requestsTotal.Load())
	fmt.Fprintf(w, "# HELP site_analyzer_analyze_requests_total Total analyze requests received.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_analyze_requests_total counter\n")
	fmt.Fprintf(w, "site_analyzer_analyze_requests_total %d\n", a.metrics.analyzeRequestsTotal.Load())
	fmt.Fprintf(w, "# HELP site_analyzer_analyze_rejected_total Analyze requests rejected because the pod is saturated.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_analyze_rejected_total counter\n")
	fmt.Fprintf(w, "site_analyzer_analyze_rejected_total %d\n", a.metrics.analyzeRejectedTotal.Load())
	fmt.Fprintf(w, "# HELP site_analyzer_analyze_errors_total Analyze requests that failed before producing a response.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_analyze_errors_total counter\n")
	fmt.Fprintf(w, "site_analyzer_analyze_errors_total %d\n", a.metrics.analyzeErrorsTotal.Load())
	fmt.Fprintf(w, "# HELP site_analyzer_active_analyses Current number of in-flight analyses.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_active_analyses gauge\n")
	fmt.Fprintf(w, "site_analyzer_active_analyses %d\n", a.metrics.activeAnalyses.Load())
	fmt.Fprintf(w, "# HELP site_analyzer_max_concurrent_analyses Configured maximum in-flight analyses per pod.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_max_concurrent_analyses gauge\n")
	fmt.Fprintf(w, "site_analyzer_max_concurrent_analyses %d\n", a.config.maxConcurrentAnalyses)
	fmt.Fprintf(w, "# HELP site_analyzer_analyze_duration_seconds Total analyze request duration.\n")
	fmt.Fprintf(w, "# TYPE site_analyzer_analyze_duration_seconds summary\n")
	fmt.Fprintf(w, "site_analyzer_analyze_duration_seconds_sum %.6f\n", durationSeconds)
	fmt.Fprintf(w, "site_analyzer_analyze_duration_seconds_count %d\n", a.metrics.analyzeDurationCount.Load())
}

func (a *app) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	a.metrics.requestsTotal.Add(1)
	a.metrics.analyzeRequestsTotal.Add(1)
	defer func() {
		a.metrics.analyzeDurationCount.Add(1)
		a.metrics.analyzeDurationNanos.Add(time.Since(start).Nanoseconds())
	}()

	if r.Method != http.MethodPost {
		respondWithError(w, http.StatusMethodNotAllowed, "Method not allowed", "Only POST requests are supported")
		return
	}
	select {
	case a.semaphore <- struct{}{}:
		a.metrics.activeAnalyses.Add(1)
		defer func() {
			<-a.semaphore
			a.metrics.activeAnalyses.Add(-1)
		}()
	default:
		a.metrics.analyzeRejectedTotal.Add(1)
		respondWithError(w, http.StatusTooManyRequests, "Too many requests", "maximum concurrent analyses are already running")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.config.maxRequestBytes)

	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		respondWithError(w, http.StatusBadRequest, "Missing URL", "url field is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.config.analysisTimeout)
	defer cancel()

	result, err := a.analyzeSite(ctx, req.URL)
	if err != nil {
		a.metrics.analyzeErrorsTotal.Add(1)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			respondWithError(w, http.StatusGatewayTimeout, "Analysis timed out", err.Error())
			return
		}
		if errors.Is(err, errUnsafeTarget) || strings.Contains(err.Error(), "url") {
			respondWithError(w, http.StatusBadRequest, "Invalid target", err.Error())
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Analysis failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (a *app) analyzeSite(ctx context.Context, rawURL string) (*AnalyzeResponse, error) {
	normalizedURL := normalizeURL(rawURL)
	parsed, err := url.Parse(normalizedURL)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid url")
	}
	if err := validateTargetURL(parsed); err != nil {
		return nil, err
	}

	domain := strings.ToLower(parsed.Hostname())
	response := &AnalyzeResponse{
		URL:        normalizedURL,
		Domain:     domain,
		AnalyzedAt: time.Now().UTC(),
		DNS: DNSInfo{
			Records: make(map[string][]string),
		},
		Security: SecurityInfo{
			Headers: make(map[string]string),
		},
		HTTP: HTTPInfo{
			Headers: make(map[string]string),
		},
	}

	var warnings []string
	if err := analyzeDNS(ctx, domain, &response.DNS, &response.Hosting); err != nil {
		if errors.Is(err, errUnsafeTarget) {
			return nil, err
		}
		warnings = append(warnings, "dns lookup failed: "+err.Error())
	}

	fetched, err := a.fetchSite(ctx, normalizedURL)
	if err != nil {
		warnings = append(warnings, "http fetch failed: "+err.Error())
	} else {
		response.FinalURL = fetched.FinalURL
		response.HTTP.StatusCode = fetched.StatusCode
		response.HTTP.Headers = selectedHeaders(fetched.Header, []string{"server", "x-powered-by", "via", "x-generator", "x-vercel-id", "cf-ray", "x-drupal-cache"})
		response.Security = analyzeSecurity(fetched)
		response.Technologies = detectTechnologies(fetched)
		if provider := detectHostingFromHeaders(fetched.Header); provider != "" {
			response.Hosting.Provider = provider
		}
		response.Hosting.CDN = detectCDN(fetched.Header)
		response.Hosting.OriginProvider, response.Hosting.OriginEvidence = detectOriginProvider(fetched)
	}

	if response.Hosting.Provider == "" {
		response.Hosting.Provider = detectHostingFromDNS(response.Hosting.ReverseDNS, response.DNS.Nameservers)
	}
	response.Warnings = warnings
	return response, nil
}

func analyzeDNS(ctx context.Context, domain string, dnsInfo *DNSInfo, hosting *HostingInfo) error {
	var firstErr error

	resolver := net.DefaultResolver
	if ips, err := resolver.LookupHost(ctx, domain); err == nil {
		for _, value := range ips {
			ip := net.ParseIP(value)
			if !isSafeIP(ip) {
				return errUnsafeTarget
			}
		}
		hosting.IPAddresses = sortedUnique(ips)
		dnsInfo.Records["A"] = filterIPs(ips, false)
		dnsInfo.Records["AAAA"] = filterIPs(ips, true)
		for _, ip := range ips {
			names, err := resolver.LookupAddr(ctx, ip)
			if err == nil {
				hosting.ReverseDNS = append(hosting.ReverseDNS, names...)
			}
		}
		hosting.ReverseDNS = sortedUnique(hosting.ReverseDNS)
	} else {
		firstErr = err
	}

	nsDomain, nsRecords, err := lookupNameservers(ctx, domain)
	if err == nil {
		for _, ns := range nsRecords {
			dnsInfo.Nameservers = append(dnsInfo.Nameservers, strings.TrimSuffix(strings.ToLower(ns.Host), "."))
		}
		dnsInfo.Nameservers = sortedUnique(dnsInfo.Nameservers)
		dnsInfo.Provider = detectDNSProvider(dnsInfo.Nameservers)
		if nsDomain != domain {
			dnsInfo.Records["NS_DOMAIN"] = []string{nsDomain}
		}
	} else if firstErr == nil {
		firstErr = err
	}

	if mxRecords, err := resolver.LookupMX(ctx, domain); err == nil {
		for _, mx := range mxRecords {
			dnsInfo.Records["MX"] = append(dnsInfo.Records["MX"], strings.TrimSuffix(strings.ToLower(mx.Host), "."))
		}
		dnsInfo.Records["MX"] = sortedUnique(dnsInfo.Records["MX"])
	}

	for recordType, values := range dnsInfo.Records {
		if len(values) == 0 {
			delete(dnsInfo.Records, recordType)
		}
	}

	return firstErr
}

func lookupNameservers(ctx context.Context, domain string) (string, []*net.NS, error) {
	labels := strings.Split(domain, ".")
	var lastErr error
	for i := 0; i <= len(labels)-2; i++ {
		candidate := strings.Join(labels[i:], ".")
		records, err := net.DefaultResolver.LookupNS(ctx, candidate)
		if err == nil && len(records) > 0 {
			return candidate, records, nil
		}
		lastErr = err
	}
	return "", nil, lastErr
}

func (a *app) fetchSite(ctx context.Context, targetURL string) (*fetchedSite, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, a.config.maxResponseBytes))
	if err != nil {
		return nil, err
	}

	return &fetchedSite{
		FinalURL:   resp.Request.URL.String(),
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       body,
		TLS:        resp.TLS != nil,
	}, nil
}

func (a *app) safeDialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, errors.New("missing host")
	}

	resolver := net.DefaultResolver
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("host resolved to no addresses")
	}

	var lastErr error
	dialer := &net.Dialer{Timeout: a.config.fetchTimeout, KeepAlive: 30 * time.Second}
	for _, resolved := range addresses {
		ip := resolved.IP
		if !isSafeIP(ip) {
			return nil, errUnsafeTarget
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errUnsafeTarget
}

func validateTargetURL(targetURL *url.URL) error {
	if targetURL == nil || targetURL.Hostname() == "" {
		return errors.New("invalid url")
	}
	if targetURL.User != nil {
		return errors.New("url credentials are not supported")
	}
	switch strings.ToLower(targetURL.Scheme) {
	case "http", "https":
	default:
		return errors.New("only http and https urls are supported")
	}
	if ip := net.ParseIP(targetURL.Hostname()); ip != nil && !isSafeIP(ip) {
		return errUnsafeTarget
	}
	return nil
}

func isSafeIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return !(ip4[0] == 169 && ip4[1] == 254)
	}
	return true
}

func analyzeSecurity(site *fetchedSite) SecurityInfo {
	securityHeaders := []string{"strict-transport-security", "content-security-policy", "x-frame-options", "x-content-type-options", "referrer-policy", "permissions-policy"}
	return SecurityInfo{
		SSL:     site.TLS,
		HSTS:    site.Header.Get("Strict-Transport-Security") != "",
		Headers: selectedHeaders(site.Header, securityHeaders),
	}
}

func detectTechnologies(site *fetchedSite) TechnologyInfo {
	detected := map[string]bool{}
	tech := TechnologyInfo{}
	lowerHTML := strings.ToLower(string(site.Body))

	if server := site.Header.Get("Server"); server != "" {
		tech.Server = server
		detected[server] = true
	}
	if poweredBy := site.Header.Get("X-Powered-By"); poweredBy != "" {
		detected[poweredBy] = true
		tech.ProgrammingLanguage = detectLanguageFromPoweredBy(poweredBy)
	}

	if strings.Contains(lowerHTML, "wp-content/") || strings.Contains(lowerHTML, "wp-includes/") || strings.Contains(lowerHTML, "/wp-json/") {
		tech.CMS = "WordPress"
		detected["WordPress"] = true
	}
	if strings.Contains(lowerHTML, "drupal-settings-json") || strings.Contains(lowerHTML, "/sites/default/") || site.Header.Get("X-Drupal-Cache") != "" {
		tech.CMS = "Drupal"
		detected["Drupal"] = true
	}
	if strings.Contains(lowerHTML, "content=\"joomla!") || strings.Contains(lowerHTML, "/media/system/js/") {
		tech.CMS = "Joomla"
		detected["Joomla"] = true
	}
	if strings.Contains(lowerHTML, "/cdn/shop/") || strings.Contains(lowerHTML, "shopify.theme") {
		tech.CMS = "Shopify"
		detected["Shopify"] = true
	}
	if strings.Contains(lowerHTML, "/umbraco/") || strings.Contains(lowerHTML, "umb-client") || strings.Contains(lowerHTML, "umbracoforms") {
		tech.CMS = "Umbraco"
		tech.ProgrammingLanguage = ".NET"
		detected["Umbraco"] = true
		detected[".NET"] = true
	}

	if strings.Contains(lowerHTML, "<!--blazor:") || strings.Contains(lowerHTML, "_framework/blazor") || strings.Contains(lowerHTML, "blazor.server.js") {
		tech.Framework = "Blazor"
		tech.ProgrammingLanguage = ".NET"
		detected["Blazor"] = true
		detected[".NET"] = true
	} else if strings.Contains(lowerHTML, "__next_data__") || strings.Contains(lowerHTML, "/_next/static/") || site.Header.Get("X-Nextjs-Cache") != "" {
		tech.Framework = "Next.js"
		detected["Next.js"] = true
		detected["React"] = true
	} else if strings.Contains(lowerHTML, "data-reactroot") || strings.Contains(lowerHTML, "react-dom") || strings.Contains(lowerHTML, "react.production.min.js") {
		tech.Framework = "React"
		detected["React"] = true
	} else if strings.Contains(lowerHTML, "__nuxt") || strings.Contains(lowerHTML, "/_nuxt/") {
		tech.Framework = "Nuxt"
		detected["Nuxt"] = true
		detected["Vue"] = true
	} else if strings.Contains(lowerHTML, "vue.js") || strings.Contains(lowerHTML, "data-v-") {
		tech.Framework = "Vue"
		detected["Vue"] = true
	} else if strings.Contains(lowerHTML, "ng-version") || strings.Contains(lowerHTML, "angular.js") {
		tech.Framework = "Angular"
		detected["Angular"] = true
	} else if strings.Contains(lowerHTML, "svelte") || strings.Contains(lowerHTML, "/_app/immutable/") {
		tech.Framework = "SvelteKit"
		detected["SvelteKit"] = true
	}
	if strings.Contains(lowerHTML, "_content/mudblazor/") || strings.Contains(lowerHTML, "mudblazor") {
		detected["MudBlazor"] = true
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(site.Body)))
	if err == nil {
		doc.Find("meta[name='generator']").Each(func(_ int, s *goquery.Selection) {
			content := strings.TrimSpace(s.AttrOr("content", ""))
			if content != "" {
				detected[content] = true
				if tech.CMS == "" {
					tech.CMS = detectCMSFromGenerator(content)
				}
			}
		})
	}

	for _, header := range []string{"CF-Ray", "X-Vercel-ID", "X-GitHub-Request-Id", "Fly-Request-Id", "X-Coolify"} {
		if site.Header.Get(header) != "" {
			detected[detectTechnologyFromHeader(header)] = true
		}
	}

	tech.Detected = mapKeys(detected)
	return tech
}

func detectDNSProvider(nameservers []string) string {
	return firstPatternMatch(nameservers, map[string]string{
		"cloudflare.com":        "Cloudflare",
		"awsdns":                "Amazon Route 53",
		"domaincontrol.com":     "GoDaddy",
		"googledomains.com":     "Google Domains",
		"google.com":            "Google Cloud DNS",
		"dnsimple.com":          "DNSimple",
		"digitalocean.com":      "DigitalOcean",
		"dnsmadeeasy.com":       "DNS Made Easy",
		"namecheap.com":         "Namecheap",
		"registrar-servers.com": "Namecheap",
		"nsone.net":             "NS1",
		"azure-dns":             "Azure DNS",
		"linode.com":            "Linode",
		"he.net":                "Hurricane Electric",
	})
}

func detectHostingFromHeaders(header http.Header) string {
	switch {
	case header.Get("CF-Ray") != "":
		return "Cloudflare"
	case header.Get("X-Vercel-ID") != "":
		return "Vercel"
	case header.Get("X-GitHub-Request-Id") != "":
		return "GitHub Pages"
	case header.Get("Fly-Request-Id") != "":
		return "Fly.io"
	case strings.Contains(strings.ToLower(header.Get("Server")), "netlify"):
		return "Netlify"
	default:
		return ""
	}
}

func detectCDN(header http.Header) string {
	switch {
	case header.Get("CF-Ray") != "" || strings.EqualFold(header.Get("Server"), "cloudflare"):
		return "Cloudflare"
	case header.Get("X-Served-By") != "" && strings.Contains(strings.ToLower(header.Get("Via")), "varnish"):
		return "Fastly"
	case header.Get("X-Cache") != "" && strings.Contains(strings.ToLower(header.Get("Via")), "cloudfront"):
		return "Amazon CloudFront"
	case header.Get("X-Akamai-Transformed") != "":
		return "Akamai"
	default:
		return ""
	}
}

func detectOriginProvider(site *fetchedSite) (string, []string) {
	lowerHTML := strings.ToLower(string(site.Body))
	lowerCSP := strings.ToLower(site.Header.Get("Content-Security-Policy"))
	lowerHeaders := strings.ToLower(flattenHeaders(site.Header))

	if strings.Contains(lowerHTML, "pantheonsite.io") || strings.Contains(lowerCSP, "pantheonsite.io") || strings.Contains(lowerHeaders, "pantheon") {
		var evidence []string
		if strings.Contains(lowerHTML, "pantheonsite.io") {
			evidence = append(evidence, "html references pantheonsite.io")
		}
		if strings.Contains(lowerCSP, "pantheonsite.io") {
			evidence = append(evidence, "content-security-policy allows pantheonsite.io")
		}
		if strings.Contains(lowerHeaders, "pantheon") {
			evidence = append(evidence, "response headers mention pantheon")
		}
		if strings.Contains(strings.ToLower(site.Header.Get("Via")), "varnish") {
			evidence = append(evidence, "via header includes varnish")
		}
		return "Pantheon", evidence
	}
	if strings.Contains(lowerHTML, "coolify") || strings.Contains(lowerHeaders, "coolify") {
		var evidence []string
		if strings.Contains(lowerHTML, "coolify") {
			evidence = append(evidence, "html mentions coolify")
		}
		if strings.Contains(lowerHeaders, "coolify") {
			evidence = append(evidence, "response headers mention coolify")
		}
		return "Coolify", evidence
	}

	return "", nil
}

func detectHostingFromDNS(reverseDNS []string, nameservers []string) string {
	provider := firstPatternMatch(reverseDNS, map[string]string{
		"amazonaws.com":         "AWS",
		"googleusercontent.com": "Google Cloud",
		"azure.com":             "Azure",
		"cloudapp.net":          "Azure",
		"digitalocean.com":      "DigitalOcean",
		"linode.com":            "Linode",
		"ovh.net":               "OVHcloud",
		"hetzner":               "Hetzner",
		"github.io":             "GitHub Pages",
		"netlify":               "Netlify",
		"vercel":                "Vercel",
	})
	if provider != "" {
		return provider
	}
	return firstPatternMatch(nameservers, map[string]string{"cloudflare.com": "Cloudflare"})
}

func detectLanguageFromPoweredBy(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "php"):
		return "PHP"
	case strings.Contains(lower, "express"):
		return "Node.js"
	case strings.Contains(lower, "next.js"):
		return "Node.js"
	case strings.Contains(lower, "asp.net"):
		return ".NET"
	default:
		return ""
	}
}

func detectCMSFromGenerator(value string) string {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "wordpress"):
		return "WordPress"
	case strings.Contains(lower, "drupal"):
		return "Drupal"
	case strings.Contains(lower, "joomla"):
		return "Joomla"
	case strings.Contains(lower, "shopify"):
		return "Shopify"
	default:
		return ""
	}
}

func detectTechnologyFromHeader(header string) string {
	switch header {
	case "CF-Ray":
		return "Cloudflare"
	case "X-Vercel-ID":
		return "Vercel"
	case "X-GitHub-Request-Id":
		return "GitHub Pages"
	case "Fly-Request-Id":
		return "Fly.io"
	case "X-Coolify":
		return "Coolify"
	default:
		return header
	}
}

func firstPatternMatch(values []string, patterns map[string]string) string {
	for _, value := range values {
		lower := strings.ToLower(value)
		for pattern, provider := range patterns {
			if strings.Contains(lower, pattern) {
				return provider
			}
		}
	}
	return ""
}

func selectedHeaders(header http.Header, names []string) map[string]string {
	selected := make(map[string]string)
	for _, name := range names {
		if value := header.Get(name); value != "" {
			selected[strings.ToLower(name)] = value
		}
	}
	return selected
}

func flattenHeaders(header http.Header) string {
	var values []string
	for name, headerValues := range header {
		values = append(values, name)
		values = append(values, headerValues...)
	}
	return strings.Join(values, " ")
}

func filterIPs(values []string, wantIPv6 bool) []string {
	var ips []string
	for _, value := range values {
		ip := net.ParseIP(value)
		if ip == nil {
			continue
		}
		isIPv6 := ip.To4() == nil
		if isIPv6 == wantIPv6 {
			ips = append(ips, value)
		}
	}
	return sortedUnique(ips)
}

func normalizeURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	return "https://" + rawURL
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		if value != "" {
			keys = append(keys, value)
		}
	}
	sort.Strings(keys)
	return keys
}

func respondWithError(w http.ResponseWriter, status int, err string, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: err, Details: details})
}
