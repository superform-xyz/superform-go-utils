package slack_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/superform-xyz/superform-go-utils/providers/slack"
)

func TestPostToPreservesSectionFieldsAndButtons(t *testing.T) {
	var body json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
	}))
	defer server.Close()
	err := slack.New().PostTo(context.Background(), server.URL, &slack.Message{Text: "Campaign created", Blocks: []slack.Block{
		{Type: "header", Text: &slack.TextObject{Type: "plain_text", Text: "Campaign created"}},
		{Type: "divider"},
		{Type: "section", Fields: []slack.TextObject{{Type: "mrkdwn", Text: "*Rewards*\n0.90 USDC"}, {Type: "mrkdwn", Text: "*Recipients*\n2"}}},
		{Type: "actions", Elements: []slack.Button{{Type: "button", Text: slack.TextObject{Type: "plain_text", Text: "View campaign"}, URL: "https://app.merkl.xyz/opportunities/123", Style: "primary"}}},
	}})
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"Campaign created","blocks":[
		{"type":"header","text":{"type":"plain_text","text":"Campaign created"}},
		{"type":"divider"},
		{"type":"section","fields":[{"type":"mrkdwn","text":"*Rewards*\n0.90 USDC"},{"type":"mrkdwn","text":"*Recipients*\n2"}]},
		{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"View campaign"},"url":"https://app.merkl.xyz/opportunities/123","style":"primary"}]}
	]}`, string(body))
}
