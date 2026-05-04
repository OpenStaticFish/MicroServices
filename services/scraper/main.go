package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
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

	http.HandleFunc("/scrape", handleScrape)
	http.HandleFunc("/health", handleHealth)

	log.Printf("scraper listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func handleScrape(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, http.StatusMethodNotAllowed, "Method not allowed", "Only POST requests are supported")
		return
	}

	var req ScrapeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid JSON", err.Error())
		return
	}

	if req.URL == "" {
		respondWithError(w, http.StatusBadRequest, "Missing URL", "url field is required")
		return
	}

	format := strings.ToLower(req.Format)
	if format == "" {
		format = "json"
	}
	if format != "md" && format != "json" {
		respondWithError(w, http.StatusBadRequest, "Invalid format", "format must be 'md' or 'json'")
		return
	}

	country := normalizeCountryCode(req.Country)
	if err := validateCountry(country); err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid country", err.Error())
		return
	}

	req.URL = normalizeURL(req.URL)
	result, err := scrapeURL(req.URL, format, country)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Scraping failed", err.Error())
		return
	}

	if format == "md" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, result.Content)
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func scrapeURL(url, format string, country string) (*ScrapeResponse, error) {
	if country != "" {
		proxy, err := selectWebshareProxy(country)
		if err != nil {
			return nil, err
		}
		transport, err := webshareProxyTransport(proxy)
		if err != nil {
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
