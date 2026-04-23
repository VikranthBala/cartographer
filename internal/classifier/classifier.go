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
}

func NewClassifier() (*Classifier, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY not set")
	}

	return &Classifier{
		apiKey: apiKey,
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

// Gemini request/response types
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

type classifierRule struct {
	Label    string `json:"label"`
	Category string `json:"category"`
}

func (c *Classifier) askGemini(host string, port int) Rule {
	if v, ok := c.cache.Load(host); ok {
		return v.(Rule)
	}

	prompt := fmt.Sprintf(`You are a network traffic classifier.
Given a hostname and port, return ONLY a JSON object with two fields:
- "label": short service name (e.g. "GitHub", "Slack", "Unknown")
- "category": one of [dev-tool, social, productivity, cloud, cdn, database, remote-access, web, network, unknown]

Hostname: %s
Port: %d

Respond with only the JSON object, no explanation.`, host, port)

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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("err do: ", err)
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

	var rule classifierRule
	if err := json.Unmarshal([]byte(geminiResp.Candidates[0].Content.Parts[0].Text), &rule); err != nil {
		return Rule{Label: "Unknown", Category: "unknown"}
	}

	result := Rule{Label: rule.Label, Category: rule.Category}
	c.cache.Store(host, result)
	return result
}

func (c *Classifier) classify(ec collector.EnrichedConn) collector.EnrichedConn {
	// 1. Port rules
	if rule, ok := c.portRules[ec.RemotePort]; ok {
		ec.ServiceLabel = rule.Label
		ec.Category = rule.Category
		return ec
	}

	// 2. Hostname suffix match
	if rule, ok := c.matchHostname(ec.RemoteHost); ok {
		ec.ServiceLabel = rule.Label
		ec.Category = rule.Category
		return ec
	}

	// 3. Gemini fallback
	rule := c.askGemini(ec.RemoteHost, ec.RemotePort)
	ec.ServiceLabel = rule.Label
	ec.Category = rule.Category
	return ec
}

func (c *Classifier) Classify(ctx context.Context, in <-chan collector.EnrichedConn, out chan<- collector.EnrichedConn) {
	for conn := range in {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out <- c.classify(conn)
	}
	close(out)
}
