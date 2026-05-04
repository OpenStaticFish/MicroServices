package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
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

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	http.HandleFunc("/analyze", handleAnalyze)
	http.HandleFunc("/health", handleHealth)

	log.Printf("site-analyzer listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, http.StatusMethodNotAllowed, "Method not allowed", "Only POST requests are supported")
		return
	}

	var req AnalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		respondWithError(w, http.StatusBadRequest, "Missing URL", "url field is required")
		return
	}

	result, err := analyzeSite(req.URL)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Analysis failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func analyzeSite(rawURL string) (*AnalyzeResponse, error) {
	normalizedURL := normalizeURL(rawURL)
	parsed, err := url.Parse(normalizedURL)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid url")
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
	if err := analyzeDNS(domain, &response.DNS, &response.Hosting); err != nil {
		warnings = append(warnings, "dns lookup failed: "+err.Error())
	}

	fetched, err := fetchSite(normalizedURL)
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

func analyzeDNS(domain string, dnsInfo *DNSInfo, hosting *HostingInfo) error {
	var firstErr error

	if ips, err := net.LookupHost(domain); err == nil {
		hosting.IPAddresses = sortedUnique(ips)
		dnsInfo.Records["A"] = filterIPs(ips, false)
		dnsInfo.Records["AAAA"] = filterIPs(ips, true)
		for _, ip := range ips {
			names, err := net.LookupAddr(ip)
			if err == nil {
				hosting.ReverseDNS = append(hosting.ReverseDNS, names...)
			}
		}
		hosting.ReverseDNS = sortedUnique(hosting.ReverseDNS)
	} else {
		firstErr = err
	}

	if nsRecords, err := net.LookupNS(domain); err == nil {
		for _, ns := range nsRecords {
			dnsInfo.Nameservers = append(dnsInfo.Nameservers, strings.TrimSuffix(strings.ToLower(ns.Host), "."))
		}
		dnsInfo.Nameservers = sortedUnique(dnsInfo.Nameservers)
		dnsInfo.Provider = detectDNSProvider(dnsInfo.Nameservers)
	} else if firstErr == nil {
		firstErr = err
	}

	if mxRecords, err := net.LookupMX(domain); err == nil {
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

func fetchSite(targetURL string) (*fetchedSite, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
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

	if strings.Contains(lowerHTML, "__next_data__") || strings.Contains(lowerHTML, "/_next/static/") || site.Header.Get("X-Nextjs-Cache") != "" {
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

	for _, header := range []string{"CF-Ray", "X-Vercel-ID", "X-GitHub-Request-Id", "Fly-Request-Id"} {
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
