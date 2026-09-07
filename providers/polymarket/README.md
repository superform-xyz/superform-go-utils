# Account-owned Polymarket credentials

Polymarket authentication has two identities. `Credentials.Address` is the
wallet that signed the ClobAuth proof and authenticates private HTTP requests.
For a POLY_1271 order, the order's maker and signer are the funding smart wallet.
Those addresses can differ; the application must authorize the relationship.

Use `utils/polymarket.AuthTypedData(address, timestamp, nonce)` to construct
the exact Polygon EIP-712 authentication message. Have the user sign it with
their wallet. `VerifyAuthSignature` checks this proof without accepting a
private key. Authentication uses EIP-712, independently of any application's
order-authorization signature format.

`providers/polymarket.NewCredentialClient` creates a separate credential
lifecycle client without enabling order submission. Pass a wallet-signed
`L1Authorization` to `CreateOrDeriveCredentials`. It makes one creation POST
and, when necessary, one derivation GET using the same signer and nonce.
It does not retry the creation POST or fall back on 401, 403, or 429.
Proofs older than five minutes or more than thirty seconds in the future
are rejected locally. HTTPS, redirect rejection, timeouts, and bounded
response sizes apply to this client.

The returned credentials must be encrypted in account-owned storage. They
are intentionally redacted by String, GoString, and MarshalJSON; applications
must use a private serialization type inside their encryption boundary.
Never send them back to a browser or log the authorization signature.

The existing trading Client interface is unchanged. Private reads validate
response structure but may return orders for multiple funding wallets belonging
to one signer. Applications must filter by an authorized maker, and check the
maker of an exact order before cancelling or reconciling it. The optional maker
argument to OpenOrder.Validate enforces that check.

`RevokeCredentials` revokes the upstream signer credential, affecting every
application or wallet sharing it. Removing one local wallet connection should
not call revocation automatically. Existing orders require explicit cancellation.

The implementation is pinned by the
[official SDK authentication vector](https://github.com/Polymarket/clob-client-v2/blob/main/tests/signing/eip712.test.ts)
and [Polymarket authentication documentation](https://docs.polymarket.com/getting-started/api).
Credential creation alone does not establish that a particular custom smart
wallet is accepted for trading by the provider.
