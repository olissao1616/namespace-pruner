package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	defaultChesBaseURL  = "https://ches.api.gov.bc.ca/api/v1"
	defaultChesTokenURL = "https://loginproxy.gov.bc.ca/auth/realms/comsvcauth/protocol/openid-connect/token"
)

// Client sends emails via CHES (Common Hosted Email Service).
// Requires env vars:
//
//	CHES_CLIENT_ID     - CHES service client ID
//	CHES_CLIENT_SECRET - CHES service client secret
//	CHES_FROM          - sender address (must be authorised in CHES)
//	CHES_BASE_URL      - override base URL (optional, for testing)
//	CHES_TOKEN_URL     - override token URL (optional, for testing)
type Client struct {
	clientID     string
	clientSecret string
	from         string
	baseURL      string
	tokenURL     string
}

func NewClient() *Client {
	baseURL := os.Getenv("CHES_BASE_URL")
	if baseURL == "" {
		baseURL = defaultChesBaseURL
	}
	tokenURL := os.Getenv("CHES_TOKEN_URL")
	if tokenURL == "" {
		tokenURL = defaultChesTokenURL
	}
	return &Client{
		clientID:     os.Getenv("CHES_CLIENT_ID"),
		clientSecret: os.Getenv("CHES_CLIENT_SECRET"),
		from:         os.Getenv("CHES_FROM"),
		baseURL:      baseURL,
		tokenURL:     tokenURL,
	}
}

type chesEmail struct {
	To       []string `json:"to"`
	From     string   `json:"from"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	BodyType string   `json:"bodyType"` // "html" or "text"
	Priority string   `json:"priority"` // "normal" | "low" | "high"
}

type chesResponse struct {
	TxID     string `json:"txId"`
	Messages []struct {
		MsgID string `json:"msgId"`
	} `json:"messages"`
}

// Send sends an HTML email to a single recipient via CHES.
func (c *Client) Send(to, subject, htmlBody string) error {
	token, err := c.fetchToken()
	if err != nil {
		return fmt.Errorf("ches token fetch failed: %w", err)
	}

	payload := chesEmail{
		To:       []string{to},
		From:     c.from,
		Subject:  subject,
		Body:     htmlBody,
		BodyType: "html",
		Priority: "normal",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("ches request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ches returned %d: %s", resp.StatusCode, string(raw))
	}

	var result chesResponse
	json.NewDecoder(resp.Body).Decode(&result)
	_ = result.TxID
	return nil
}

// fetchToken gets a Bearer token from the CHES OIDC provider using client credentials.
func (c *Client) fetchToken() (string, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}

	resp, err := http.Post(c.tokenURL, "application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("empty access_token in response")
	}
	return result.AccessToken, nil
}
