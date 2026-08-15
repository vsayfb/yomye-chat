package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type CoreClient struct {
	target *url.URL
	http   *http.Client
}

type Listing struct {
	ID     string        `json:"id"`
	Author ListingAuthor `json:"author"`
}

type ListingAuthor struct {
	ID string `json:"id"`
}

func NewCoreClient(rawTarget string, timeout time.Duration) (*CoreClient, error) {
	target, err := parseOrigin(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid --core-target: %w", err)
	}
	return &CoreClient{
		target: target,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

func (c *CoreClient) ListingsByAuthor(ctx context.Context, authorID string) ([]Listing, error) {
	query := url.Values{
		"author_id": []string{authorID}, "base_category": []string{"gig"},
		"country_code": []string{"TR"}, "limit": []string{"50"},
	}
	endpoint := c.target.ResolveReference(&url.URL{Path: "/core/listings", RawQuery: query.Encode()})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Language", "tr")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Core listing request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Core listing request status=%d", resp.StatusCode)
	}
	var envelope successEnvelope[[]Listing]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, errors.New("decode Core listing response")
	}
	if envelope.Status != http.StatusOK {
		return nil, errors.New("Core listing response envelope is malformed")
	}
	return envelope.Data, nil
}

func parseOrigin(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("target must be an absolute http or https origin")
	}
	if target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		(target.Path != "" && target.Path != "/") {
		return nil, errors.New("target must contain only scheme, host, and optional port")
	}
	target.Path = ""
	return target, nil
}
