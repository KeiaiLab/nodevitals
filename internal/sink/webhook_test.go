package sink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nodevitals/nodevitals/internal/config"
	"github.com/nodevitals/nodevitals/internal/model"
)

func TestWebhookPostsSignedCloudEvent(t *testing.T) {
	var gotBody []byte
	var gotSig, gotType, gotID, gotTs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("Webhook-Signature")
		gotType = r.Header.Get("Content-Type")
		gotID = r.Header.Get("Webhook-Id")
		gotTs = r.Header.Get("Webhook-Timestamp")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook(config.WebhookConfig{URL: srv.URL, Secret: "shh"}, srv.Client())
	ev := model.Event{Node: "n", Tier: "core", Device: "cpu", Condition: "load_high", Phase: model.PhaseEnter}
	ev.ID = "abc-ENTER-1"

	if err := wh.EmitEvents(context.Background(), []model.Event{ev}); err != nil {
		t.Fatalf("EmitEvents: %v", err)
	}

	if gotType != "application/cloudevents+json" {
		t.Fatalf("content-type = %q", gotType)
	}
	if gotID != ev.ID {
		t.Fatalf("Webhook-Id = %q, want %q", gotID, ev.ID)
	}
	if gotTs == "" {
		t.Fatal("Webhook-Timestamp header missing")
	}
	parsedTs, err := strconv.ParseInt(gotTs, 10, 64)
	if err != nil {
		t.Fatalf("Webhook-Timestamp not an int: %v", err)
	}
	if want := Sign("shh", gotID, parsedTs, gotBody); gotSig != want {
		t.Fatalf("signature mismatch: got %q want %q", gotSig, want)
	}
	var ce CloudEvent
	if err := json.Unmarshal(gotBody, &ce); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if ce.SpecVersion != "1.0" || ce.Type != "com.nodevitals.hw.event.v1" {
		t.Fatalf("bad envelope: %+v", ce)
	}
}

func TestWebhookNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wh := NewWebhook(config.WebhookConfig{URL: srv.URL, Secret: "s"}, srv.Client())
	err := wh.EmitEvents(context.Background(), []model.Event{{Condition: "x"}})
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestSignIsStandardWebhooksV1Format(t *testing.T) {
	sig := Sign("whsec_test", "msg_1", 1700000000, []byte("payload"))
	if !strings.HasPrefix(sig, "v1,") {
		t.Fatalf("must use Standard Webhooks v1 prefix, got %q", sig)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sig, "v1,"))
	if err != nil {
		t.Fatalf("after v1, must be base64: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("HMAC-SHA256 must be 32 bytes, got %d", len(raw))
	}
	// every input must be signed (the old body-only bug would fail these)
	if Sign("other", "msg_1", 1700000000, []byte("payload")) == sig {
		t.Fatal("signature must depend on secret")
	}
	if Sign("whsec_test", "msg_2", 1700000000, []byte("payload")) == sig {
		t.Fatal("signature must depend on id")
	}
	if Sign("whsec_test", "msg_1", 1700000001, []byte("payload")) == sig {
		t.Fatal("signature must depend on timestamp")
	}
	if Sign("whsec_test", "msg_1", 1700000000, []byte("payload2")) == sig {
		t.Fatal("signature must depend on body")
	}
}
