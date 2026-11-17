// Models the v7 contract documented at https://docs.sushi.com/api/examples/swap.
// Prices and abbreviated calldata are fixtures, not executable swap data.
const http = require('http');

http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  const params = url.searchParams;
  const reply = (status, body) => {
    res.writeHead(status, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify(body));
  };
  if (req.method !== 'GET' || !/^\/(quote|swap)\/v7\/1$/.test(url.pathname)) {
    return reply(501, { title: 'Endpoint not modeled' });
  }
  const native = '0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
  const sushi = '0x6b3595068778dd592e39a122f4f5a5cf09c90fe2';
  if (params.get('tokenIn')?.toLowerCase() !== native ||
      params.get('tokenOut')?.toLowerCase() !== sushi ||
      params.get('amount') !== '1000000000000000' ||
      params.get('maxSlippage') !== '0.005') {
    return reply(422, { title: 'Unexpected fixture request parameters' });
  }

  // These referrer strings select local test fixtures only. The real API treats
  // referrer as attribution, not as a failure-injection mechanism.
  switch (params.get('referrer')) {
    case 'fixture:no-route': return reply(200, { status: 'NoWay' });
    case 'fixture:partial': return reply(200, { status: 'Partial' });
    case 'fixture:rate-limit':
      res.setHeader('Retry-After', '2');
      return reply(429, {
        type: 'https://docs.sushi.com/api/errors/ratelimit-exceeded',
        title: 'Ratelimit Exceeded', status: 429,
      });
  }

  const response = {
    status: 'Success',
    tokens: [
      { address: native, decimals: 18, symbol: 'ETH', name: 'Ether' },
      { address: sushi, decimals: 18, symbol: 'SUSHI', name: 'SushiToken' },
    ],
    tokenFrom: 0, tokenTo: 1,
    swapPrice: 11951.002273851282, priceImpact: 0.003650867404904279,
    amountIn: '1000000000000000', assumedAmountOut: '11951002273851281408',
    gasSpent: 77000,
  };
  if (url.pathname.startsWith('/swap/')) {
    if (!/^0x[\da-f]{40}$/i.test(params.get('sender') || '') ||
        !['true', 'false'].includes(params.get('simulate'))) {
      return reply(422, { title: 'Missing sender or simulate parameter' });
    }
    response.tx = {
      from: params.get('sender'), to: '0xac4c6e212a361c968f1725b4d055b47e63f80b75',
      data: '0x5f3bd1c800000000', value: '1000000000000000', gasPrice: 47396891,
    };
    if (params.get('simulate') === 'true') response.tx.gas = '123456';
  }
  return reply(200, response);
}).listen(process.env.PORT || 4500);
