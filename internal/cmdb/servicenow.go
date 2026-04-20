package cmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

type Client struct {
	baseURL  string
	username string
	password string
	table    string
	cluster  string
}

type Namespace struct {
	Name        string `json:"name"`
	ClusterURL  string `json:"cluster_url"`
	OwnerEmail  string `json:"owner_email"`
	Environment string `json:"environment"`
}

type snResponse struct {
	Result []Namespace `json:"result"`
}

func NewClient() *Client {
	return &Client{
		baseURL:  os.Getenv("SERVICENOW_URL"),
		username: os.Getenv("SERVICENOW_USER"),
		password: os.Getenv("SERVICENOW_PASS"),
		table:    os.Getenv("SERVICENOW_TABLE"),
		cluster:  os.Getenv("CLUSTER_URL"),
	}
}

// GetNamespaces returns namespaces belonging to this cluster only.
func (c *Client) GetNamespaces() ([]Namespace, error) {
	url := fmt.Sprintf(
		"%s/api/now/table/%s?sysparm_fields=name,cluster_url,owner_email,environment&sysparm_query=cluster_url=%s",
		c.baseURL, c.table, c.cluster,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("servicenow request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("servicenow returned %d", resp.StatusCode)
	}

	var result snResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("servicenow decode failed: %w", err)
	}
	return result.Result, nil
}
