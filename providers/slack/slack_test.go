package slack_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/superform-xyz/superform-go-utils/providers/slack"
)

func TestPostToSendsMessage(t *testing.T) {
	want := &slack.Message{
		Text: "Campaign created ✅",
		Blocks: []slack.Block{{Type: "section", Text: &slack.TextObject{
			Type: "mrkdwn", Text: "*Campaign created*",
		}}},
		Attachments: []slack.Attachment{{
			Title: "Referral fees", Color: "#17a2b8",
			Fields: []slack.Field{{Title: "Rewards", Value: "0.90 USDC", Short: true}},
		}},
	}
	wantJSON := `{"text":"Campaign created ✅","blocks":[{"type":"section","text":{"type":"mrkdwn","text":"*Campaign created*"}}],"attachments":[{"title":"Referral fees","color":"#17a2b8","fields":[{"title":"Rewards","value":"0.90 USDC","short":true}]}]}`
	var expected map[string]any
	if err := json.Unmarshal([]byte(wantJSON), &expected); err != nil {
		t.Fatal(err)
	}
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s, %s", r.Method, r.Header.Get("Content-Type"))
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("decode message: %v", err)
		}
		got <- msg
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	if err := slack.New(slack.WithHTTPClient(srv.Client())).PostTo(context.Background(), srv.URL, want); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; !reflect.DeepEqual(msg, expected) {
		t.Fatalf("message = %#v, want %#v", msg, expected)
	}
}

func TestPostToRejectsHTTPFailuresWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private response body"))
			}))
			defer srv.Close()
			err := slack.New().PostTo(context.Background(), srv.URL, &slack.Message{Text: "test"})
			if err == nil || strings.Contains(err.Error(), "private response body") {
				t.Fatalf("unexpected error: %v", err)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want 1", requests.Load())
			}
		})
	}
}

func TestPostToDoesNotFollowRedirects(t *testing.T) {
	var forwarded atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
	}))
	defer sink.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	if err := slack.New().PostTo(context.Background(), srv.URL, &slack.Message{Text: "private message"}); err == nil {
		t.Fatal("expected redirect error")
	}
	if forwarded.Load() != 0 {
		t.Fatal("message followed redirect")
	}
}

func TestPostToPreservesCancellationWithoutLeakingWebhook(t *testing.T) {
	const secretURL = "https://hooks.slack.com/services/private-webhook-credential"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := slack.New().PostTo(ctx, secretURL, &slack.Message{Text: "test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if strings.Contains(err.Error(), "private-webhook-credential") {
		t.Fatalf("webhook credential leaked: %v", err)
	}
}
