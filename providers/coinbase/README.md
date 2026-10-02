# Coinbase Onramp client

The client accepts CDP Ed25519 or P-256 API keys and signs each request for its HTTP method, host and path. It supports hosted session tokens, transaction history, buy URL construction and read-only catalogue discovery.

- `GetBuyConfig(ctx)` returns supported countries, US subdivisions and payment method IDs.
- `GetBuyOptions(ctx, GetBuyOptionsRequest{Country: "US", Subdivision: "CA"})` returns payment currencies with per-method limits and purchase assets with their networks. Country/subdivision codes are normalized; US requests require a subdivision.
- `BuyLimit.Min` and `Max` are decimal strings denominated in their enclosing `BuyFiat.ID`, not a common reference currency.
- `BuyNetwork.ChainID` preserves Coinbase's JSON string representation. Consumers selecting an output must check the asset, network, chain ID and contract address together.

Missing/null discovery lists return an error; explicit empty lists represent no supported options. Caching, budgets, country policy and executable payment-method selection belong to the caller.

Run `go test ./providers/coinbase/...` for local fixtures and signer tests. For live discovery, inject `COINBASE_API_KEY_ID` and `COINBASE_API_KEY_SECRET` into the environment and run:

```sh
COINBASE_LIVE_TEST=1 go test -count=1 -v ./providers/coinbase -run '^TestLiveCatalog$'
```

The opt-in test makes at most five read-only requests: config, then options for US/CA, US/NY, GB and DE. It verifies native USDC on Base and creates no checkout session or payment. Never put real credentials in fixtures or source files.
