package debank

// filterTokens filters tokens based on whether they are verified or core
// note: verified tokens are prioritized over core tokens but Debank does not provide a verified flag for some.
// example DAI on BASE: https://pro-openapi.debank.com/v1/token?chain_id=base&id=0x50c5725949a6f0c72e6c4a641f24049a917db0cb
// in the example, is_verified is null, so it's unmarshalled as false
func tokenFilter(token Token) bool {
	if token.IsVerified {
		return token.IsVerified
	}

	return token.IsCore // if not verified, return if it's a core token
}
