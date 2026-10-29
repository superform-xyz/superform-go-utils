# Hyperliquid SDK

`providers/hyperliquid` owns `/info` and signed `/exchange` transport, L1
Explorer reads, wire types, canonical action encoding, signature verification
and exact decimal parsing; `providers/hyperliquid/hypercore` owns HyperEVM
precompile encoding, raw reads, paired reads, CoreWriter event decoding and
exact unit conversion.
Both packages accept injected clients and have no dependency on application
configuration, databases, Redis, logging, metrics or protobuf types.

## HTTP reads

Construct `NewInfoClient(endpoint, httpClient, options...)` with the caller's
HTTP client; a nil client uses a five-second timeout. An empty endpoint selects
the public mainnet info endpoint, so applications requiring explicit
configuration must validate it in their adapter. Named methods cover account
state, spot state, agents, perp and spot metadata, order status, user request
allowance and raw non-funding ledger pages.
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
to 512 bytes. Signed exchange writes use the separate `ExchangeClient` and
retain their own durable dispatch lifecycle.

Wire decimal strings are retained without floating-point conversion.
`ParseAmount` optionally converts decimal text to an exact `big.Rat`.
No wire type decides account eligibility, spot prices, capital flows or PnL.

`NonFundingLedgerUpdatesPage` returns one page, with an optional end time. It
does not sort, deduplicate, require hashes or infer completeness from a fixed
page size. Raw rows and deltas retain their original JSON, including unknown
fields. `DecodeLedgerDelta` provides typed variants with exact `Decimal`
values, while the existing flat wire view retains its string fields. Callers
choose their own evidence validation, pagination and checkpoint rules; a
cash-flow collector and an execution reconciler can need different identities.

`RawExtraAgents` preserves address text so callers can handle malformed entries
individually; `ExtraAgents` retains its validated `common.Address` shape.
Order status and rate-limit responses are provider facts. Authorization,
expiry decisions and interpreting a missing order remain caller policy.

## L1 Explorer and CoreWriter evidence

`NewL1ExplorerClient(endpoint, httpClient, options...)` accepts the same injected
transport and response limit as info reads. It decodes user, transaction and
complete-block responses, validates their identities and preserves unknown L1
actions. It performs one attempt per request. Callers own polling windows,
causal attribution, persistence and lifecycle transitions.

The `hypercore` package decodes version-one CoreWriter payloads, `RawAction`
logs and receipt evidence. Known payloads must be canonical ABI encodings;
unknown action IDs retain their bytes. Receipt decoding validates hashes,
block identity and log provenance and returns any valid evidence alongside
errors from other occurrences. A queued CoreWriter action alone does not
establish its HyperCore application outcome.

`TokenSystemAddress` includes HYPE's special system address. The ntl helpers
convert exact six-decimal USD values; `CoreWeiToAmount` scales token units
without assigning the token a USD price. Supported-token lists, enrollment
limits and funding thresholds belong to applications.

## Signed actions and exchange requests

Ordered order/cancel wire structs, `EncodeAction`, `ConnectionID`, `AgentDigest`
and `RecoverActionSigner` implement the venue's signing protocol. Signing
sources are explicit (`SigningSourceMainnet` or `SigningSourceTestnet`) and
independent of the account's EVM chain ID. `VerifyActionSignature` returns the
signer; the caller must check that signer against its approved-agent policy.
No private key is required by this package. Reconstruct typed actions after
storage: JSON object key order is not a substitute for signed msgpack order.
`WithActionEncodingLimit` lets applications retain their own size limit.

`DecodeEnvelope` and `NewExchangeRequest` handle signature scalars and wire
serialization. Callers enforce mandatory expiry, nonce ownership, account
eligibility and immutable action identity before submitting.

`NewExchangeClient(endpoint, httpClient, options...)` copies the injected
`http.Client`, disables redirects and defaults to a 4 MiB response limit.
`WithExchangeResponseLimit` changes that limit. Its `Post` method accepts a
pre-dispatch callback, invoked after request construction and immediately
before `Do`; use that boundary to persist a durable dispatch marker. A callback
failure prevents the HTTP attempt. `Response.Dispatched` means `Do` was invoked
and does not prove server receipt or execution. Failures after dispatch must
retain that uncertainty. The SDK neither retries nor maps responses to
application verdicts, and transport errors and non-success bodies do not echo
signed payloads or endpoint credentials. An injected RoundTripper must also
avoid implicit retries. Applications retain budgets, clock fences, recovery,
redaction of provider prose and batch lifecycle decisions.

## Using the SDK from another repository

Import the provider directly; no Bundler or Persephone package is required:

```go
import (
    "context"
    "net/http"
    "time"

    "github.com/ethereum/go-ethereum/common"
    hl "github.com/superform-xyz/superform-go-utils/providers/hyperliquid"
)

func readAccount(ctx context.Context, endpoint string, user common.Address) (*hl.PerpState, error) {
    client := hl.NewInfoClient(endpoint, &http.Client{Timeout: 5 * time.Second})
    return client.ClearinghouseState(ctx, user)
}

func verifyOrder(order hl.OrderWire, nonce, expiry int64, signature []byte) (string, error) {
    action := hl.NewOrderAction(order)
    return hl.VerifyActionSignature(action, nonce, "", &expiry,
        hl.SigningSourceMainnet, signature, hl.WithActionEncodingLimit(16*1024))
}
```

For request accounting, `InfoClient.Post` accepts the exported wire response
types and returns dispatch, status and venue Date metadata. Wrap that single
attempt with the application's limiter and budget rather than adding another
provider HTTP implementation.

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
HTTP attempt accounting and response limits, ledger unions, CoreWriter and
Explorer evidence, independent signature vectors and exchange dispatch
boundaries. Consumer tests retain their application fallback, budgets, caches,
authorization, recovery and publication behavior.

Publish a utils revision containing these packages before updating consumers'
root-utils requirements. While developing unpublished changes, use temporary
module files with a local replacement; keep machine-specific replacements out
of consumer `go.mod` files. Verify each consumer against its own pinned
dependencies, overlaying only this provider when the working utils checkout
contains unrelated changes. Local replacement tests alone do not make a
consumer ready to deploy with its old published dependency pin.
