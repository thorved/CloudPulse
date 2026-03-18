package cloudflare

import "context"

type DNSRecord struct {
	ID      string
	Name    string
	Type    string
	Content string
	TTL     int
	Proxied bool
}

type DNSProvider interface {
	ListRecords(ctx context.Context, name string) ([]DNSRecord, error)
	CreateARecord(ctx context.Context, name, ip string, ttl int, proxied bool) (DNSRecord, error)
	UpdateRecord(ctx context.Context, recordID string, ttl int, proxied bool) error
	DeleteRecord(ctx context.Context, recordID string) error
}
