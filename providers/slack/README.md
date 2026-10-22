# Slack incoming webhooks

`providers/slack` owns webhook HTTP delivery and Slack message wire types.
Callers own webhook secrets, channel routing, message content, delivery policy,
and logging.

```go
client := slack.New()
err := client.PostTo(ctx, webhookURL, &slack.Message{Text: "Campaign created"})
```

The default client has a ten-second timeout, rejects redirects, and sends one
request per call without automatic retries. Messages support text, blocks, and
attachments. Include a text summary with structured messages for notification
previews and screen readers.

`WithHTTPClient` accepts a caller-owned HTTP client for custom transports,
network restrictions, and testing; its timeout and redirect policy apply as
configured by the caller. Transport errors omit the webhook URL, and HTTP
failures report the status without including response bodies or credentials.

Consumers should pin a published utils revision containing this package before
merging their migration. For local cross-repo verification, use a temporary Go
workspace containing this module and the consumer modules; do not commit local
`replace` directives or machine-specific paths.
