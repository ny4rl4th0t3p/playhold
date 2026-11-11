# Run in production

A playhold server starts in production mode. It refuses settings that would expose players, and it won't serve over
plain HTTP unless a proxy in front of it ends TLS. This page covers TLS, proxies, the allowed web origins, and the
development mode for your own machine. What the server needs from whatever runs it is in [Deploy](./deploy.md).

Every snippet calls `run`, which is how a game's `main` function serves a `Server`:

<<< ../../example_test.go#run

## Development mode

On a developer's machine, `Development` turns the safety checks off, so the server runs over plain HTTP:

<<< ../../example_test.go#development

The server logs a warning at startup while it's on. Never set it on a server players reach.

Without it:

- `NewServer` refuses an origin pattern that matches every site, such as `*`, and a trusted proxy range that covers
  every address;
- a connection that arrives without TLS, and not through a trusted proxy, is refused with HTTP 403;
- `ListenAndServe` won't start with neither TLS nor a trusted proxy.

## TLS

`ListenAndServe` serves the game with TLS in one of three ways. It also answers health checks at `/healthz` and
`/readyz`, and stops cleanly when its context ends.
See [Keep games running](./keeping-games-running.md#health-checks-drain-and-shutdown).

### Automatic certificates

With a domain that points at the server, and port 443 reachable from the internet, the server gets its certificates from
Let's Encrypt:

<<< ../../example_test.go#autocert

How it works:

- The server asks for a certificate when the first player connects, not when it starts. That first connection waits a
  few seconds.
- It answers Let's Encrypt's challenge on port 443 itself, so nothing needs to listen on port 80.
- It keeps certificates in `AutocertCacheDir`, so a restart doesn't ask for new ones. Let's Encrypt limits how often a
  domain can ask.
- If Let's Encrypt can't reach the server, for example because the domain points elsewhere or port 443 is closed, the
  connection fails and the server's log says why.

### Test the setup first

Let's Encrypt blocks a domain for a while after a few failed requests. Try a new setup against their test service
first. It works the same way, but browsers don't trust its certificates:

<<< ../../example_test.go#staging

From another terminal, an issuer with `(STAGING)` in its name means the setup works:

```bash
curl -kv https://game.example/healthz 2>&1 | grep issuer
```

Then switch to the real service by removing `AutocertDirectoryURL`. Keep the two cache folders apart: certificates are
kept by domain name, so a shared folder would keep serving the test certificate.

### A certificate you already have

<<< ../../example_test.go#certificate-files

The files are read at start, so a renewed certificate takes a restart, which loses no match.

### Behind a proxy

A proxy or load balancer that ends TLS sends the server plain HTTP. List its addresses in `TrustedProxies`:

<<< ../../example_test.go#behind-a-proxy

Without TLS fields, the server listens on port 8080. A connection from a trusted proxy may arrive without TLS, and the
client's address comes from the proxy's `X-Forwarded-For` header: the rightmost address that isn't a trusted proxy. From
anywhere else, that header is ignored, since a client could make it up. For a proxy that sends another header, such as
`Forwarded`, set `ProxyClientIP`.

The client's address matters for the [limits](./limits.md) that count per address.

## Web origins

A browser says which page opened a connection. The server's own host is always allowed. To let a page hosted elsewhere
connect, such as a game on itch.io, list its origins in `OriginPatterns`, such as `*.itch.zone`. A pattern that matches
every origin, such as `*`, is refused outside development mode.