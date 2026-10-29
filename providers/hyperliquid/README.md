# Hyperliquid read SDK

`providers/hyperliquid` owns `/info` HTTP transport, wire types, address
validation and exact decimal parsing; `providers/hyperliquid/hypercore` owns
HyperEVM precompile encoding, raw reads, paired reads and exact unit conversion.
Both packages accept injected clients and have no dependency on application
configuration, databases, Redis, logging, metrics or protobuf types.

## HTTP reads

Construct `NewInfoClient(endpoint, httpClient, options...)` with the caller's
HTTP client; a nil client uses a five-second timeout. An empty endpoint selects
the public mainnet info endpoint, so applications requiring explicit
configuration must validate it in their adapter. Named methods cover account
state, spot state, agents, metadata/contexts and raw non-funding ledger pages.
`Post` supports other info request/response shapes without another transport.

There are no automatic retries, budgets or caches. Applications retain request
weight reservation, fleet limits, pagination/checkpoints, source selection,
fallback, freshness and valuation rules. An injected transport must also avoid
implicit retries when exact attempt accounting matters.

`Post` returns response metadata even on failure: `Dispatched` records whether
the transport's `Do` was invoked, `StatusCode` records an HTTP response, and
`Date` retains the venue clock header. Dispatch does not prove server receipt.
`RequestError.Kind`, `HTTPError` and `ErrRateLimited` let adapters preserve their
own instrumentation and retry decisions. Successful JSON responses default to
a 4 MiB limit, configurable with `WithResponseLimit`; error bodies are limited
to 512 bytes. This client is for reads; signed exchange writes retain their
separate durable dispatch lifecycle.

Wire decimal strings are retained without floating-point conversion.
`ParseAmount` optionally converts decimal text to an exact `big.Rat`.
No wire type decides account eligibility, spot prices, capital flows or PnL.

## HyperCore reads

The precompile package accepts a minimal `EthCaller`, a concrete EVM address
and explicit DEX/token indices. `spotBalance` accepts its full `uint64` token
argument; `tokenInfo` uses `uint32`. Account value and raw USD are signed
`int64`; margin, notional, spot and withdrawable values are unsigned `uint64`.
USD values use six decimal places; spot values use their token's `WeiDecimals`.
Do not assume `TokenInfo.EvmContract` is an ERC-20 address: the captured USDC
metadata identifies its gateway instead.

`ReadMarginAndSpot` and `ReadPerpSnapshot` (margin plus withdrawable) each make
one `aggregate3` call at latest. Both reject incomplete or malformed pairs and
never retry through separate calls. Applications accepting sequential fallback
must make that choice explicitly and retain the loss of atomicity. RPC block
tags and independently fetched EVM heads do not establish historical HyperCore
provenance; these results carry no block number.

Precompile output must have canonical ABI encoding and the expected widths.
Withdrawable is read directly; it is not derived from equity minus margin.
Margin-summary fields do not provide per-position unrealized PnL.
Exact EVM/Core conversion helpers reject overflow and nonzero remainders;
explicit `Floor` variants return the discarded remainder.

## Verification and adoption

From the root utils module:

```sh
go test -race ./providers/hyperliquid/...
```

Protocol tests include captured values, independent Multicall layouts, signed
and unsigned boundaries, malformed outputs, partial failures, cancellation,
HTTP attempt accounting and response limits. Consumer tests retain their
application fallback, budgets, cache and publication behavior.

Publish a utils revision containing these packages before updating consumers'
root-utils requirements. While developing unpublished changes, use temporary
module files with a local replacement; keep machine-specific replacements out
of consumer `go.mod` files. Verify each consumer against its own pinned
dependencies, overlaying only this provider when the working utils checkout
contains unrelated changes. Local replacement tests alone do not make a
consumer ready to deploy with its old published dependency pin.
