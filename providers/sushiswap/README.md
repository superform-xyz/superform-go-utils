# SushiSwap provider

Go client for Sushi's v7 exact-input quote and swap APIs. It follows the other
DEX providers' `Client`, constructor options, shared HTTP transport, and generated
test mock conventions. It fetches unsigned transactions; callers handle approvals
and transaction submission.

```go
client, err := sushiswap.New() // no API key required
if err != nil {
    return err
}
defer client.Close()

swap, err := client.GetSwap(ctx, sushiswap.SwapRequest{
    ChainID:     1,
    TokenIn:     common.Address{}, // native ETH; 0xEeee… also works
    TokenOut:    common.HexToAddress("0x6B3595068778DD592e39A122f4f5a5cF09C90fE2"),
    Amount:      big.NewInt(1_000_000_000_000_000), // base units
    SlippageBps: 50, // 0.5%, sent as maxSlippage=0.005
    Sender:      sender,
    Recipient:   recipient, // omit to receive at sender
})
if err != nil {
    return err
}
// swap.Tx contains To, From, Data, Value and available gas fields.
```

`GetQuote` takes `QuoteRequest` with the same chain, tokens, amount, slippage, and
optional referrer. Both endpoints reject incomplete routes with `ErrNoRoute` or
`ErrPartialRoute`; use `errors.Is` to classify these failures.

Sushi returns `assumedAmountOut`, with **no separate minimum-output field** in
either endpoint. `EstimatedAmountOutMin` is calculated locally as
`floor(assumedAmountOut * (10000 - slippageBps) / 10000)`. It is an estimate;
Sushi's returned calldata determines the enforced on-chain minimum.

`WithAPIKey(key)` sends the optional `apiKey` query parameter. Other options are
`WithBaseURL`, `WithHTTPClient`, `WithTimeout`, and `WithRetry`. Timeout and retry
options configure the built-in HTTP client; an injected HTTP client uses its own
settings. Requests are not retried by default. HTTP errors remain inspectable
with `http_client.ResponseStatus` and `http_client.RetryAfter`.

Simulation is explicitly disabled by default so fetching calldata does not
require the sender to hold tokens or grant approvals. Set `Simulate: true` to
request Sushi's balance/allowance checks and a simulated gas limit. This does not
send a transaction.

## Verification

```sh
go test -race ./providers/sushiswap/...
SUSHISWAP_LIVE=1 go test ./providers/sushiswap -run '^TestLive$' -v
```

The opt-in live check fetches an ETH-to-SUSHI quote and unsigned calldata without
an API key. It never broadcasts a transaction.

For repeatable HTTP integration checks, start the contract-based local stand-in
from the repository root:

```sh
postman simulation run postman/simulations/sushiswap/sushiswap.sim.yaml --port 4010
```

In another terminal:

```sh
SUSHISWAP_SIMULATION_URL=http://127.0.0.1:4010/sushiswap \
  go test ./providers/sushiswap -run '^TestSimulation$' -v
```

The stand-in checks the request parameters and models success, no route, partial
route, and rate-limit responses. Its fixture selectors are local test controls,
not Sushi API features. Its short example calldata is a parsing fixture and must
not be submitted to a chain. The local simulation requires no Postman login.

Contract sources:

- [Quote API](https://docs.sushi.com/api/examples/quote)
- [Swap API and response fields](https://docs.sushi.com/api/examples/swap)
- [Swap SDK request fields](https://docs.sushi.com/sdk/guides/swap)
- [Official SDK validation](https://github.com/sushi-labs/sushi/blob/master/src/evm/api/swap.ts)
- [HTTP error types](https://docs.sushi.com/api/errors)
