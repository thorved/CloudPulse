package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudflare/cloudflare-go/v6/option"
)

func TestClientListRecordsUsesAuthHeaderAndResolvesZone(t *testing.T) {
	t.Parallel()

	var zonesCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected auth header: %q", got)
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			switch call := zonesCalls.Add(1); call {
			case 1:
				if got := r.URL.Query().Get("name"); got != "app.example.com" {
					t.Fatalf("unexpected first zone query name: %q", got)
				}
				writeEnvelope(t, w, []map[string]any{})
			case 2:
				if got := r.URL.Query().Get("name"); got != "example.com" {
					t.Fatalf("unexpected second zone query name: %q", got)
				}
				writeEnvelope(t, w, []map[string]any{
					{
						"id":   "zone-1",
						"name": "example.com",
					},
				})
			default:
				t.Fatalf("unexpected extra zone lookup %d for %s", call, r.URL.Query().Get("name"))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			if got := r.URL.Query().Get("name.exact"); got != "app.example.com" {
				t.Fatalf("unexpected query name: %q", got)
			}
			writeEnvelope(t, w, []map[string]any{
				{
					"id":      "record-1",
					"type":    "A",
					"name":    "app.example.com",
					"content": "1.2.3.4",
					"ttl":     60,
					"proxied": false,
				},
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(
		"token",
		"",
		server.Client(),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)

	records, err := client.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Content != "1.2.3.4" {
		t.Fatalf("expected record content 1.2.3.4, got %q", records[0].Content)
	}
	if zonesCalls.Load() != 2 {
		t.Fatalf("expected two zone resolution calls, got %d", zonesCalls.Load())
	}

	_, err = client.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records with cached zone: %v", err)
	}
	if zonesCalls.Load() != 2 {
		t.Fatalf("expected cached zone id to avoid extra lookups, got %d zone calls", zonesCalls.Load())
	}
}

func TestClientRetriesRetryableResponses(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			writeEnvelope(t, w, nil)
			return
		}

		writeEnvelope(t, w, []map[string]any{
			{
				"id":      "record-1",
				"type":    "A",
				"name":    "app.example.com",
				"content": "1.2.3.4",
				"ttl":     60,
				"proxied": false,
			},
		})
	}))
	defer server.Close()

	client := NewClient(
		"token",
		"zone",
		server.Client(),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(1),
	)

	records, err := client.ListRecords(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record after retry, got %d", len(records))
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestClientCreateUpdateDelete(t *testing.T) {
	t.Parallel()

	var (
		createCalled bool
		updateCalled bool
		deleteCalled bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			writeEnvelope(t, w, []map[string]any{
				{
					"id":   "zone-1",
					"name": "example.com",
				},
			})
		case r.Method == http.MethodPost:
			createCalled = true
			writeEnvelope(t, w, map[string]any{
				"id":      "record-1",
				"type":    "A",
				"name":    "app.example.com",
				"content": "1.2.3.4",
				"ttl":     60,
				"proxied": false,
			})
		case r.Method == http.MethodGet:
			writeEnvelope(t, w, map[string]any{
				"id":      "record-1",
				"type":    "A",
				"name":    "app.example.com",
				"content": "1.2.3.4",
				"ttl":     60,
				"proxied": false,
			})
		case r.Method == http.MethodPatch:
			updateCalled = true
			writeEnvelope(t, w, map[string]any{
				"id":      "record-1",
				"type":    "A",
				"name":    "app.example.com",
				"content": "1.2.3.4",
				"ttl":     60,
				"proxied": false,
			})
		case r.Method == http.MethodDelete:
			deleteCalled = true
			writeEnvelope(t, w, map[string]any{
				"id": "record-1",
			})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClient(
		"token",
		"",
		&http.Client{Timeout: 2 * time.Second},
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)

	record, err := client.CreateARecord(context.Background(), "app.example.com", "1.2.3.4", 60, false)
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	if record.ID != "record-1" {
		t.Fatalf("expected created record ID record-1, got %q", record.ID)
	}
	if err := client.UpdateRecord(context.Background(), "record-1", 60, false); err != nil {
		t.Fatalf("update record: %v", err)
	}
	if err := client.DeleteRecord(context.Background(), "record-1"); err != nil {
		t.Fatalf("delete record: %v", err)
	}

	if !createCalled || !updateCalled || !deleteCalled {
		t.Fatalf("expected create/update/delete to be called, got create=%v update=%v delete=%v", createCalled, updateCalled, deleteCalled)
	}
}

func TestClientListRecordsFailsWhenNoMatchingZoneIsAccessible(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/zones" {
			if got := r.URL.Query().Get("name"); got == "" {
				t.Fatal("expected zone lookup to include name")
			}
			writeEnvelope(t, w, []map[string]any{})
			return
		}

		t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := NewClient(
		"token",
		"",
		server.Client(),
		option.WithBaseURL(server.URL),
		option.WithMaxRetries(0),
	)

	_, err := client.ListRecords(context.Background(), "app.example.com")
	if err == nil {
		t.Fatal("expected zone resolution error")
	}
	if !strings.Contains(err.Error(), "no accessible Cloudflare zone matched the hostname") {
		t.Fatalf("expected missing zone error, got %v", err)
	}
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"errors":  []any{},
		"result":  result,
	}); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
