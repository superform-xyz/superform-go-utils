# Everlong

Client for the [Everlong Swap API](https://router.foreverlong.io/swap-api/guide), hosted at `https://swap.foreverlong.io`. Supports Katana (`747474`) only. The API key is required and sent in `x-api-key`, never in the URL. Obtain a production key from the provider; the documented public test key is not a default.

`GetQuote` calls `GET /v1/quote`. `GetSwap` calls `GET /v1/swap` with `recipient` and integer `slippage` in basis points (1–1000). Amounts are exact-input token base units. The API has no independent sender or simulation parameter. No signing, approvals, broadcasting, or balance checks are performed by this client.

Responses must match the requested input amount and token addresses and include a complete route. Both `Success` (Swagger/live) and `Ok` (the guide) are accepted. Missing or malformed swap transactions are rejected. `AssumedAmountOut` is already net of the provider fee; the client does not deduct it again. `EstimatedAmountOutMin` is a local estimate after slippage; returned calldata controls execution and is preserved exactly.

The bundled success fixture was captured from a read-only Katana vbUSDC → vbETH swap request. Run `go test ./providers/everlong/... -short -race` for local HTTP checks. The optional live check runs with `EVERLONG_API_KEY` and without `-short`.
