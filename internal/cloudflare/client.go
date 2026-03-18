package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/cloudflare/cloudflare-go/v6"
	cfdns "github.com/cloudflare/cloudflare-go/v6/dns"
	"github.com/cloudflare/cloudflare-go/v6/option"
	cfzones "github.com/cloudflare/cloudflare-go/v6/zones"
)

type Client struct {
	zoneID string
	client *sdk.Client
	mu     sync.Mutex
}

func NewClient(apiToken, zoneID string, httpClient *http.Client, opts ...option.RequestOption) *Client {
	requestOptions := []option.RequestOption{
		option.WithAPIToken(apiToken),
	}
	if httpClient != nil {
		requestOptions = append(requestOptions, option.WithHTTPClient(httpClient))
	}
	requestOptions = append(requestOptions, opts...)

	return &Client{
		zoneID: zoneID,
		client: sdk.NewClient(requestOptions...),
	}
}

func (c *Client) ListRecords(ctx context.Context, name string) ([]DNSRecord, error) {
	zoneID, err := c.zoneIDForName(ctx, name)
	if err != nil {
		return nil, err
	}

	page, err := c.client.DNS.Records.List(ctx, cfdns.RecordListParams{
		ZoneID: sdk.F(zoneID),
		Name: sdk.F(cfdns.RecordListParamsName{
			Exact: sdk.F(name),
		}),
		PerPage: sdk.F(1000.0),
	})
	if err != nil {
		return nil, fmt.Errorf("list dns records: %w", err)
	}

	records := make([]DNSRecord, 0, len(page.Result))
	for _, record := range page.Result {
		records = append(records, DNSRecord{
			ID:      record.ID,
			Name:    record.Name,
			Type:    string(record.Type),
			Content: record.Content,
			TTL:     int(record.TTL),
			Proxied: record.Proxied,
		})
	}

	return records, nil
}

func (c *Client) CreateARecord(ctx context.Context, name, ip string, ttl int, proxied bool) (DNSRecord, error) {
	zoneID, err := c.zoneIDForName(ctx, name)
	if err != nil {
		return DNSRecord{}, err
	}

	record, err := c.client.DNS.Records.New(ctx, cfdns.RecordNewParams{
		ZoneID: sdk.F(zoneID),
		Body: cfdns.ARecordParam{
			Name:    sdk.F(name),
			TTL:     sdk.F(cfdns.TTL(ttl)),
			Type:    sdk.F(cfdns.ARecordTypeA),
			Content: sdk.F(ip),
			Proxied: sdk.F(proxied),
		},
	})
	if err != nil {
		return DNSRecord{}, fmt.Errorf("create dns record: %w", err)
	}

	return DNSRecord{
		ID:      record.ID,
		Name:    record.Name,
		Type:    string(record.Type),
		Content: record.Content,
		TTL:     int(record.TTL),
		Proxied: record.Proxied,
	}, nil
}

func (c *Client) UpdateRecord(ctx context.Context, recordID string, ttl int, proxied bool) error {
	zoneID, err := c.cachedZoneID()
	if err != nil {
		return err
	}

	current, err := c.client.DNS.Records.Get(ctx, recordID, cfdns.RecordGetParams{
		ZoneID: sdk.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("get dns record before edit: %w", err)
	}

	_, err = c.client.DNS.Records.Edit(ctx, recordID, cfdns.RecordEditParams{
		ZoneID: sdk.F(zoneID),
		Body: cfdns.RecordEditParamsBody{
			Name:    sdk.F(current.Name),
			TTL:     sdk.F(cfdns.TTL(ttl)),
			Type:    sdk.F(cfdns.RecordEditParamsBodyType("A")),
			Content: sdk.F(current.Content),
			Proxied: sdk.F(proxied),
		},
	})
	if err != nil {
		return fmt.Errorf("edit dns record: %w", err)
	}

	return nil
}

func (c *Client) DeleteRecord(ctx context.Context, recordID string) error {
	zoneID, err := c.cachedZoneID()
	if err != nil {
		return err
	}

	_, err = c.client.DNS.Records.Delete(ctx, recordID, cfdns.RecordDeleteParams{
		ZoneID: sdk.F(zoneID),
	})
	if err != nil {
		return fmt.Errorf("delete dns record: %w", err)
	}

	return nil
}

func (c *Client) zoneIDForName(ctx context.Context, hostname string) (string, error) {
	c.mu.Lock()
	if c.zoneID != "" {
		zoneID := c.zoneID
		c.mu.Unlock()
		return zoneID, nil
	}
	c.mu.Unlock()

	normalized := normalizeHostname(hostname)
	candidates := zoneCandidates(normalized)

	for _, candidate := range candidates {
		page, err := c.client.Zones.List(ctx, cfzones.ZoneListParams{
			Name:    sdk.F(candidate),
			PerPage: sdk.F(50.0),
		})
		if err != nil {
			return "", fmt.Errorf("resolve zone id for %s: %w", hostname, err)
		}

		for _, zone := range page.Result {
			if strings.EqualFold(normalizeHostname(zone.Name), candidate) {
				c.mu.Lock()
				c.zoneID = zone.ID
				c.mu.Unlock()
				return zone.ID, nil
			}
		}
	}

	return "", fmt.Errorf("resolve zone id for %s: no accessible Cloudflare zone matched the hostname; set cloudflare.zone_id explicitly if needed", hostname)
}

func (c *Client) cachedZoneID() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.zoneID == "" {
		return "", fmt.Errorf("cloudflare zone id is not resolved yet; call a hostname-based operation first")
	}

	return c.zoneID, nil
}

func normalizeHostname(hostname string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
}

func zoneCandidates(hostname string) []string {
	labels := strings.Split(normalizeHostname(hostname), ".")
	candidates := make([]string, 0, len(labels)-1)

	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		if candidate == "" {
			continue
		}
		candidates = append(candidates, candidate)
	}

	return candidates
}
