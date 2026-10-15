# superform-go-utils consolidation audit (2026-10-15)

Scope: read-only review of superform-utils and the repos that use it: persephone, bundler, erebor, superman-strategy, plus superman-oms because strategy and OMS share copy-pasted code. Paths are relative to each repo.

How each repo uses utils today:

| Repo | Utils version | Notes |
|---|---|---|
| persephone | HEAD (`057596a`) | heaviest provider user (merkl, zerion, transak, debank, …) |
| bundler | `5674368` (Sept) | constants, filter, http_client, swap and price providers |
| erebor | `17242aa` (Oct 6) | `Ptr`, snapshotd, kyberswap, openocean, defillama, axiom; never uses `http_client` or `backoff` |
| superman-oms | `5674368` | similar to bundler |
| superman-strategy | — | doesn't import utils at all; rebuilds HTTP, retry and providers by hand |

## 1. Code duplicated across repos that should move into utils

### Vendor clients written more than once

| Vendor | Existing copies | Move to |
|---|---|---|
| **Across** | erebor `internal/infrastructure/across/client.go:75`, bundler `bridge-svc/domain/bridge/across/*` (both confirmed) | `providers/across`, with a typed `ErrRouteNotEnabled` |
| **Pendle** | persephone `pkg/integrations/pendle/api`, erebor `provider/pendle/pendle.go`, OMS `swap/pendle/client.go` | `providers/pendle` |
| **Persephone price API** | bundler `pkg/persephone`, erebor `infrastructure/persephone` | `providers/persephone` |
| **Erebor API** | strategy `erebor_pps.go` and `discovery.go`, OMS `infra/erebor/client.go` | `providers/erebor` |
| **Superform fair-price / positions API** | strategy `fairprice_pps.go:61` (`ChainID int`), OMS `pricing/client.go:125` (`int64`, extra provenance fields) | `providers/superform_pricing`; the two wire types have already drifted |
| **Etherscan / explorers** | persephone `pkg/data/explorers`, bundler `pkg/explorer` | `providers/etherscan` |
| **Hyperliquid** | persephone `pkg/data/hyperliquid`, bundler `pkg/hypercore/info.go`, `explorer.go` | `providers/hyperliquid` |
| **Slack / incident.io** | bundler `pkg/slack`, `pkg/incidentIO`; erebor slack/webhook; persephone incidentio | `providers/slack`, `providers/incidentio` |
| **Goldsky / subgraph GraphQL** | bundler `pkg/subgraph` (with pagination), erebor `subgraph/client.go`, strategy `graphql.go`, persephone (4 copies, incl. two near-identical `supervaultbalances` clients) | `pkg/subgraph` |
| **LiFi** | two copies inside persephone datamat | `providers/lifi` |

Vendor clients with no utils provider yet (one repo each):

- **persephone:** coingecko, goplus
- **erebor:** morpho, vaults.fyi
- **bundler:** deBridge, LayerZero, Tenderly

### Generic helpers copied everywhere

| Helper | Evidence | Suggested home |
|---|---|---|
| **10^decimals, wei↔decimal/float** | ~40 inline copies in persephone (`rawAmountToFloat` ×2, `bigIntToFloat` ×2); erebor ×6; strategy `pow10Int` ×2+; bundler `pkg/utils/token_utils.go`, `ethutils.go`, `ScaleBigInt` (utils already has a private copy in `oneinch/types.go:167`) | new `utils/units` or `utils/bigmath`: `Pow10`, `ToDecimal`, `ToFloat`, `Rescale`, `ScaleBigInt`, bps helpers (erebor `internal/pkg/bps`) |
| **Address normalization** | 10+ `normalizeAddress` copies in persephone datamat; erebor `NormalizeAddress`, `LowerAddresses` | `utils/address` (or `constants`) |
| **Native `0xeeee…` redeclared** | persephone ×6, erebor kyberswap, OMS | use `constants.GetNativeToken` (and fix the bug in section 2 first) |
| **Retry loops** | strategy ×3, erebor ×4+, OMS `infra/erebor/client.go:295` (OMS already imports `backoff`), bundler `subsidise_svc.go:193`, persephone `retryWithBackoff`, plus 6× sleep-with-context | `pkg/backoff`: add a max-delay cap, jitter and `SleepCtx` |
| **Bugs in hand-rolled retries** | erebor `onchain/verifier.go:74` and bundler `subsidise_svc.go:193` use `time.Sleep` and ignore ctx | `pkg/backoff`; these are real bugs today |
| **Retry-After / retryable-status parsing** | persephone ×6 (superbundler, expo, zerion, coingecko, goplus, tokenuniverse) | `pkg/http_client` |
| **Metrics RoundTripper** | persephone ×5 (yahoo, finnhub, sec, frankfurter, debank), erebor `metrics/http_transport.go` | `http_client` metrics hook via `SetTransportWrapper` |
| **Slice/map generics** | `chunkStrings` ×6 in persephone, bundler `pkg/utils/map.go` (`Keys` etc.), dedupe copies everywhere | `utils/filter`: `Chunk[T]`, `Keys`, `SortedKeys` |
| **Pointer helpers** | `ptrBool`/`floatPtr`/`IntPtr`/… in all 5 repos, even repos that already import `utils.Ptr` (erebor, OMS) | `utils.Ptr`, plus new `Deref`/`ValueOr` |
| **Multicall3 / ERC20 reads** | bundler `pkg/multicall3` (+ a second copy in `hypercore/multicall.go`, also used by OMS), persephone pendle multicall + `rpcbalance`, erebor `sessionkey_multicall.go`, strategy hand-coded `balanceOf` selectors | `utils/multicall3`, `utils/erc20` |
| **Chain slug maps** | erebor pendle/morpho/vaultsfyi, strategy defillama/config, persephone goldsky/coingecko/goplus | each vendor's slug map in its provider; chain IDs from `constants` |
| **Signatures / EIP-712 / Permit2, KMS signer, revert/event decoding, viper config hooks, `LoadYAML`** | bundler `pkg/utils/crypto.go`, `permit2`, `ethawskmssigner`, `evmrevert`, `confighooks`, ~15 `LoadChainsFromFile` copies | `utils/crypto`, `pkg/signer`, `utils/abi`, `pkg/config` (only if a second repo needs them) |
| **Strategy ↔ OMS copy-paste** | `api/httputil` (only comments differ), `middleware/authn.go`, `principal`, `logging` | shared `pkg/service` (or a small superman-common module) |

## 2. Utils changes so clients stop working around it

### `pkg/http_client`

These are the gaps that force clients to wrap or bypass it:

- **No Retry-After support.** It retries 429s on a fixed doubling delay. That's why persephone transak hardcodes 1 minute, and why zerion, coingecko and goplus roll their own retry.
- **Only one auth header (`SetAuth`).** OMS `BuildRetryHTTPClient` loops over several headers, and each call overwrites the last.
- **`SetTimeout` covers all retries together**, because it's the `http.Client.Timeout`. OMS works around it with `PerDoHTTPTimeout`. Add a per-attempt timeout.
- **Every other non-2xx status is retried, including 404 and 422.** It should be a configurable retry policy.
- **Missing builder options** that clients currently bolt on themselves:
  - User-Agent (erebor `provider/httpclient.go`, for Cloudflare)
  - no-redirect (persephone coinbase and frankfurter inject a raw `http.Client` and lose retries)
  - max response size (strategy uses `LimitReader` everywhere)
  - rate limiter (erebor vaults.fyi, persephone defillama)
  - SSRF-safe dialer (erebor `netsec`)
- **No `GetJSON[T]` / `PostJSON[T]` helper**, so every client repeats get → read → decode → close.
- **Dead code in clients:** since non-2xx is already an error, `StatusCode != 200` checks can never fire (persephone explorers; bundler across, hypercore, persephone, subgraph). bundler `across/quote.go:48` also matches error strings instead of using `ResponseStatus`.

Tested and ruled out: retried POSTs do replay their body correctly, so no change is needed there.

### `utils/constants`

- **Bug, confirmed by a test:** `IsNativeToken("0xeeee…")` with a lowercase string returns `false`, because it compares against the checksummed `Hex()`. persephone avoids it with its own `EqualFold` checks (`services/vault/internal/service/token.go:1290`, `pkg/nativeassets/nativeassets.go:95`). Fix it with `HexToAddress` or `EqualFold`. `IsNullAddress` is not affected, because the zero address has no letters to checksum.
- `IsNativeToken` already treats the null address as native, yet bundler writes `IsNativeToken(x) || IsNullAddress(x)` in 4 places. Document that behaviour.
- Add `Multicall3Address` and `Pow10`.
- Strategy uses `int64` chain IDs and utils uses `uint64`, so strategy needs a type change to adopt `constants`.

### Providers (fix at the source rather than in each client)

- **debank:**
  - persephone builds its own client with `SetAuth("AccessKey")`, while the provider sets that header itself (`providers/debank/debank.go:346`). The side effect is that persephone's injected client silently swaps the provider's no-retry default for 3 retries.
  - persephone `metrics.go:147` type-asserts `*http_client.Transport` to rewrite `BaseTransport`; the provider should take a transport-wrapper option instead.
  - The 100-id limit (`pkg/data/debank/token_api.go:59`) and the Stable/Arc tokens DeBank omits (bundler `balance_providers/debank/types.go:26`) should move into the provider.
  - bundler re-wraps the interface and copies the `Token` type with a custom `UnmarshalJSON`.
- **defillama:**
  - Missing endpoints: `/block`, `/chart`, max-age search.
  - Missing chains: stable, sonic, worldchain, mantle, linea, bera, gnosis, …
  - `Coin.Price` is a `float64` and can't parse string prices.
  - Results: three raw clients in persephone, a full copy in strategy, and an interface re-wrap in bundler.
- **oneinch:**
  - `Swap` has no `ToAmountMin` (`Quote` does), so bundler recomputes it (`hooks_data.go:48`).
  - bundler also repeats the provider's token normalization, validation and retry defaults.
- **kyberswap / openocean:**
  - They should return `ToAmountMin`, handle native↔null mapping, and take `SlippageBps int`.
  - Today that logic is reimplemented in erebor (`kyberswap/client.go:105`, `openocean/client.go:160`) and bundler (`kyberswap/quote.go:73`, `openocean/quote.go:167`).
- **yahoo:** export `stockDetailFromChart` and `priceBarsFromChart` (`providers/yahoo/yahoo.go:324,348`). persephone has copies at `pkg/data/yahoo/yahoo.go:115-260`.
- **rhinestone:** model the `"all" | []Token` union properly; persephone hand-parses `json.RawMessage` (`catalog.go:121`).
- **zerion / frankfurter / merkl:**
  - Add the zerion fungibles API (persephone `pkg/data/zerion/client.go`, 270 lines).
  - Add frankfurter `GetLatestRates`.
  - strategy `datafeed/client/merkl.go` duplicates `merkl.GetUserRewards`; it can switch over now.
- **Mocks:** erebor (`.mockery.yaml:148`) and bundler (`pkg/debank/mock`, `pkg/defillama/mock`) generate their own mocks of utils interfaces. Add `mocks/` to transak, frankfurter and merkl, and drop the local copies.
- **`abi_encoder.go`:** add a `MustTupleType`; bundler re-implements it in `hypercore/precompiles.go:82` and in one place ignores the error.

### Build risk

superman-oms ships a copy of superbundler under `third_party/`. Its `dex-svc/.../odos` imports `providers/odos`, which utils removed in `553413f`, and OMS pins a utils version from after that removal. The OMS build passes only because nothing in OMS imports dex-svc today.

## Suggested order

1. Fix the `IsNativeToken` bug and the two `time.Sleep` retry bugs.
2. `http_client`: Retry-After, multiple headers, per-attempt timeout, User-Agent/no-redirect/max-size/rate-limit options, `GetJSON`.
3. A `units`/`bigmath` package plus `filter.Chunk`/`Keys`, `Deref` and `address.Normalize` (the copies are everywhere).
4. Bring strategy onto utils (defillama, merkl, `http_client`, `backoff`).
5. New providers, most duplicated first: across, pendle, persephone/erebor/superform_pricing, etherscan, hyperliquid, subgraph.
6. Move per-client quirks into debank, defillama, oneinch, kyberswap and openocean, and delete the local wrappers and mocks.

Most file:line references above come from parallel scans of each repo. The debank double-auth, the debank metrics type-assert, the two Across clients and the oneinch `ToAmountMin` gap were re-checked by hand.
