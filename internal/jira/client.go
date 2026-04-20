package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type Client struct {
	baseURL string
	email   string
	token   string
	project string
}

func NewClient() *Client {
	return &Client{
		baseURL: os.Getenv("JIRA_URL"),
		email:   os.Getenv("JIRA_EMAIL"),
		token:   os.Getenv("JIRA_TOKEN"),
		project: os.Getenv("JIRA_PROJECT"),
	}
}

type issueFields struct {
	Project     struct{ Key string }   `json:"project"`
	Summary     string                 `json:"summary"`
	IssueType   struct{ Name string }  `json:"issuetype"`
	Description *adfDocument          `json:"description"`
	Labels      []string               `json:"labels"`
}

type issuePayload struct {
	Fields issueFields `json:"fields"`
}

// adfDocument is the Atlassian Document Format required by JIRA v3 API.
type adfDocument struct {
	Version int      `json:"version"`
	Type    string   `json:"type"`
	Content []adfNode `json:"content"`
}

type adfNode struct {
	Type    string     `json:"type"`
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

func (c *Client) CreateTicket(namespace, imageRef, ruleType, severity string, ageDays int) (string, error) {
	var p issuePayload
	p.Fields.Project.Key = c.project
	p.Fields.IssueType.Name = "Bug"
	p.Fields.Summary = fmt.Sprintf("[Pruner] %s violation in %s: %s", ruleType, namespace, imageRef)
	p.Fields.Labels = []string{"pruner", "image-hygiene", ruleType}
	p.Fields.Description = textDoc(fmt.Sprintf(
		"Severity: %s\nNamespace: %s\nImage: %s\nRule: %s\nAge: %d days\n\nPlease remediate within SLA.",
		severity, namespace, imageRef, ruleType, ageDays,
	))

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
		return "", fmt.Errorf("jira returned %d", resp.StatusCode)
	}

	var result struct {
		Key string `json:"key"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Key, nil
}
