package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
)

type Client struct {
	baseURL string
	email   string
	token   string
	project string

	// cache accountId lookups so we don't hit the API on every violation
	accountCache   map[string]string
	accountCacheMu sync.Mutex
}

func NewClient() *Client {
	return &Client{
		baseURL:      os.Getenv("JIRA_URL"),
		email:        os.Getenv("JIRA_EMAIL"),
		token:        os.Getenv("JIRA_TOKEN"),
		project:      os.Getenv("JIRA_PROJECT"),
		accountCache: make(map[string]string),
	}
}

type issueFields struct {
	Project     struct{ Key string }  `json:"project"`
	Summary     string                `json:"summary"`
	IssueType   struct{ Name string } `json:"issuetype"`
	Description *adfDocument         `json:"description"`
	Labels      []string              `json:"labels"`
	Assignee    *assignee             `json:"assignee,omitempty"`
}

type assignee struct {
	AccountID string `json:"accountId"`
}

type issuePayload struct {
	Fields issueFields `json:"fields"`
}

// adfDocument is the Atlassian Document Format required by JIRA v3 API.
type adfDocument struct {
	Version int       `json:"version"`
	Type    string    `json:"type"`
	Content []adfNode `json:"content"`
}

type adfNode struct {
	Type    string      `json:"type"`
	Content []adfInline `json:"content,omitempty"`
}

type adfInline struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func textDoc(body string) *adfDocument {
	return &adfDocument{
		Version: 1,
		Type:    "doc",
		Content: []adfNode{{
			Type: "paragraph",
			Content: []adfInline{{
				Type: "text",
				Text: body,
			}},
		}},
	}
}

// CreateTicket creates a JIRA issue and assigns it to the namespace owner.
// ownerEmail comes from ServiceNow CMDB.
func (c *Client) CreateTicket(namespace, imageRef, ruleType, severity, ownerEmail string, ageDays int) (string, error) {
	var p issuePayload
	p.Fields.Project.Key = c.project
	p.Fields.IssueType.Name = "Bug"
	p.Fields.Summary = fmt.Sprintf("[Pruner] %s violation in %s: %s", ruleType, namespace, imageRef)
	p.Fields.Labels = []string{"pruner", "image-hygiene", ruleType}
	p.Fields.Description = textDoc(fmt.Sprintf(
		"Severity: %s\nNamespace: %s\nImage: %s\nRule: %s\nAge: %d days\nOwner: %s\n\nPlease remediate within SLA.",
		severity, namespace, imageRef, ruleType, ageDays, ownerEmail,
	))

	// Look up the owner's Atlassian account ID and assign the issue to them.
	// If lookup fails we still create the ticket — just unassigned.
	if ownerEmail != "" {
		if accountID, err := c.lookupAccountID(ownerEmail); err == nil && accountID != "" {
			p.Fields.Assignee = &assignee{AccountID: accountID}
		}
	}

	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", c.baseURL+"/rest/api/3/issue", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("jira request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("jira returned %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Key string `json:"key"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Key, nil
}

// lookupAccountID resolves an email address to an Atlassian Account ID.
// Results are cached in memory for the lifetime of the run.
func (c *Client) lookupAccountID(email string) (string, error) {
	c.accountCacheMu.Lock()
	if id, ok := c.accountCache[email]; ok {
		c.accountCacheMu.Unlock()
		return id, nil
	}
	c.accountCacheMu.Unlock()

	endpoint := fmt.Sprintf("%s/rest/api/3/user/search?query=%s", c.baseURL, url.QueryEscape(email))
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.email, c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("user search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("user search returned %d", resp.StatusCode)
	}

	var users []struct {
		AccountID    string `json:"accountId"`
		EmailAddress string `json:"emailAddress"`
		Active       bool   `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}

	// Pick the first active user whose email matches exactly.
	for _, u := range users {
		if u.Active && u.EmailAddress == email {
			c.accountCacheMu.Lock()
			c.accountCache[email] = u.AccountID
			c.accountCacheMu.Unlock()
			return u.AccountID, nil
		}
	}
	return "", fmt.Errorf("no active user found for %s", email)
}
