# Rhinestone discovery client

This package owns HTTP transport, project-key authentication and response types
for `GET /onramp/options` and `GET /chains` on Rhinestone's deposit service.
It creates no orders or payments and does not cache or retry requests.

```go
client, err := rhinestone.New(rhinestone.WithAPIKey(projectKey))
if err != nil {
    return err
}
defer client.Close()

options, err := client.GetOnrampOptions(ctx, rhinestone.GetOnrampOptionsRequest{
    Provider: "swapped",
    Kind:     "fiat",
    Country:  trustedCountry,
})
```

`Country` is optional, but supplying it asserts the user's country to
Rhinestone. Establish trust in the consumer before setting it; do not promote
unverified user input. Chain discovery never sends this header.

The default client has a five-second timeout. `WithHTTPClient` accepts custom
transport/timeouts and `WithAPIBaseURL` overrides the deposit-service root.
Redirects are rejected even with a custom client. Response bodies are capped
at 8 MiB; HTTP errors expose only their status and no vendor body or credential.

Method identifiers, fiat lists, decimal limit text, nullable fields and the
chain token union are preserved. Unknown methods remain available to callers;
this package does not assign canonical payment mechanisms or infer eligibility.
Swapped currently returns `bank-transfer` for its manual EUR transfer;
`google-pay` must only be offered when project discovery returns it.

Persephone owns trusted geography, shared capability caches, request budgets,
USDC destination checks, payment-method mapping, FX validation and provider
selection under `pkg/data/rhinestone` and `pkg/onramp`.

Run `go test -race ./providers/rhinestone` for deterministic HTTP tests.
