package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vikranthBala/cartographer/internal/collector"
)

type Rule struct {
	Label    string
	Category string
}

type Classifier struct {
	cache     sync.Map
	portRules map[int]Rule
	hostRules map[string]Rule
	apiKey    string
	client    *http.Client
}

func NewClassifier() (*Classifier, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}

	return &Classifier{
		apiKey: apiKey,
		client: &http.Client{
			Timeout: 5 * time.Second, // Prevent API hangs from breaking the pipeline
		},
		portRules: map[int]Rule{
			22:    {Label: "SSH", Category: "remote-access"},
			53:    {Label: "DNS", Category: "network"},
			80:    {Label: "HTTP", Category: "web"},
			443:   {Label: "HTTPS", Category: "web"},
			5432:  {Label: "PostgreSQL", Category: "database"},
			3306:  {Label: "MySQL", Category: "database"},
			6379:  {Label: "Redis", Category: "database"},
			27017: {Label: "MongoDB", Category: "database"},
		},
		hostRules: map[string]Rule{
			"github.com":            {Label: "GitHub", Category: "dev-tool"},
			"githubusercontent.com": {Label: "GitHub", Category: "dev-tool"},
			"google.com":            {Label: "Google", Category: "productivity"},
			"googleapis.com":        {Label: "Google APIs", Category: "dev-tool"},
			"googleusercontent.com": {Label: "Google", Category: "cdn"},
			"fbcdn.net":             {Label: "WhatsApp/Facebook", Category: "social"},
			"facebook.com":          {Label: "Facebook", Category: "social"},
			"whatsapp.com":          {Label: "WhatsApp", Category: "social"},
			"amazonaws.com":         {Label: "AWS", Category: "cloud"},
			"cloudflare.com":        {Label: "Cloudflare", Category: "cdn"},
			"fastly.net":            {Label: "Fastly", Category: "cdn"},
		},
	}, nil
}

func (c *Classifier) matchHostname(host string) (Rule, bool) {
	if host == "" {
		return Rule{}, false
	}
	for pattern, rule := range c.hostRules {
		if strings.HasSuffix(host, pattern) {
			return rule, true
		}
	}
	return Rule{}, false
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content geminiContent `json:"content"`
	} `json:"candidates"`
}

func (c *Classifier) askGemini(host string, port int) Rule {
	key := fmt.Sprintf("%s:%d", host, port)
	if v, ok := c.cache.Load(key); ok {
		return v.(Rule)
	}

	prompt := fmt.Sprintf(`You are a network traffic classifier.
Given a hostname and port, return ONLY a JSON object with:
- "label": short service name
- "category": one of ["infra","dev","social","cdn","unknown"]

Hostname: %s
Port: %d`, host, port)

	body, err := json.Marshal(geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	})
	if err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}

	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-flash-latest:generateContent"
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-goog-api-key", c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}
	defer resp.Body.Close()

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return Rule{Label: "Unknown", Category: "unknown"}
	}

	text := geminiResp.Candidates[0].Content.Parts[0].Text
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")

	var rule Rule
	if err := json.Unmarshal([]byte(text), &rule); err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}

	c.cache.Store(key, rule)
	return rule
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimSuffix(host, ".")
	host = strings.ToLower(host)
	return host
}

func (c *Classifier) classify(ec collector.EnrichedConn) collector.EnrichedConn {
	host := normalizeHost(ec.RemoteHost)

	if host != "" {
		if rule, ok := c.matchHostname(host); ok {
			ec.ServiceLabel = rule.Label
			ec.Category = rule.Category
			return ec
		}
	}

	if rule, ok := c.portRules[ec.RemotePort]; ok {
		ec.ServiceLabel = rule.Label
		ec.Category = rule.Category
		return ec
	}

	if host == "" {
		if ec.RemoteAddr.IsLoopback() {
			ec.ServiceLabel = "Loopback"
			ec.Category = "network"
			return ec
		}
		if ec.RemoteAddr.IsPrivate() {
			ec.ServiceLabel = "Local Network"
			ec.Category = "network"
			return ec
		}
		ec.ServiceLabel = "Unknown"
		ec.Category = "unknown"
		return ec
	}

	rule := c.askGemini(host, ec.RemotePort)
	ec.ServiceLabel = rule.Label
	ec.Category = rule.Category

	return ec
}

// Classify uses a worker pool to prevent slow Gemini lookups from blocking the pipeline
func (c *Classifier) Classify(ctx context.Context, in <-chan collector.EnrichedConn, out chan<- collector.EnrichedConn) {
	sem := make(chan struct{}, 10) // Allow 10 concurrent requests
	var wg sync.WaitGroup

	for conn := range in {
		select {
		case <-ctx.Done():
			break
		default:
		}

		sem <- struct{}{}
		wg.Add(1)

		go func(ec collector.EnrichedConn) {
			defer wg.Done()
			defer func() { <-sem }()

			result := c.classify(ec)

			select {
			case out <- result:
			case <-ctx.Done():
			}
		}(conn)
	}

	wg.Wait()
	close(out)
}
