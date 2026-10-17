# CLAUDE.md

## 🤖 Welcome, Claude

This repository uses **`AGENTS.md`** as the single source of truth for:

* Coding conventions (naming, formatting, commenting, testing)
* Contribution workflows (branch prefixes, commit message style, PR templates)
* Release, CI, and dependency‑management policies
* Security reporting and governance links

> **TL;DR:** **Read `AGENTS.md` first.**
> All technical or procedural questions are answered there.

### Quick Checklist for Claude

1. **Study `AGENTS.md`**
   Make sure every automated change or suggestion respects those rules.
2. **Follow branch‑prefix and commit‑message standards**
   The `pull-request-management.yml` workflow labels each PR from its branch prefix and title
   keywords (for example, `feat/` → `feature`, `fix/` → `bug-P3`) and adds a size label; CI gates
   rely on them too.
3. **Never tag releases**
4. **Pass CI**
   Run `magex format:fix`, `magex lint` and `magex test:race` locally before opening a PR.

If you encounter conflicting guidance elsewhere, `AGENTS.md` wins, with one exception: the project
invariants below override the generic `tech-conventions` where they differ (for example, tests here
never call `t.Parallel`).
Questions or ambiguities? Open a discussion or ping a maintainer instead of guessing.

Happy hacking!

---

## About This Project

`go-serper` is a small Go SDK for three **Serper** operations: web search (`POST /search`) and image
search (`POST /images`) on `https://google.serper.dev`, and page scrape (`POST /`) on the separate
`https://scrape.serper.dev` origin, plus offline `Decode*` helpers for bodies kept with raw capture. It is
an independent, unofficial library, not an application: there is no binary, no CLI, and no runtime
configuration file. Serper publishes no API reference: the shapes come from its playground and examples.

- Module: `github.com/mrz1836/go-serper` (package `serper`, one flat package, one method per operation
  on `*Client`)
- Go version: 1.26 (CI runs 1.26.x as the primary version and 1.27.x as the secondary, so tests cover
  both the encoding/json v1 and v2 implementations)
- Runtime dependencies: none (`testify` is used only by tests and `internal/testutil`)

**Invariants that changes must preserve:**
- **No runtime dependencies.** Library code imports only the standard library, and never `log`, `os`,
  `unsafe`, `compress/gzip`, `net/http/httputil` or `net/http/httptrace`. The `depguard` and
  `forbidigo` linters enforce this.
- **Exactly one HTTP attempt per call.** No retries, redirects, hedging, fallbacks, pagination, batching
  or caching. Every request body is a plain reader with `GetBody` nil and no idempotency key, so the
  standard library cannot resend it; the SDK's own transport speaks HTTP/1.1 only.
- **The API key goes only to the configured origins,** in the `X-API-KEY` header only. Origins must be
  validated `https` origins; redirects are refused and their `Location` is stripped. The key lives only
  inside a function value the `Client` holds.
- **No values in errors.** `Error()` holds only the operation, kind, HTTP status, a fixed transport class
  and a JSON location: never the key, queries, scrape targets, bodies, headers or provider text.
  `Metadata` is safe to log.
- **Raw capture is explicit and bounded.** Bodies are kept only with `WithRawCapture` or
  `WithDefaultRawCapture`, never printed or marshaled, and `RawJSON` always returns a new copy.
- **No logging, globals (other than sentinel errors) or `init`, no background goroutines, no sleeps, and
  no environment reads** in SDK code. The only code that runs apart from the caller is the
  `context.AfterFunc` that closes a response body on timeout or cancellation.
- **Unit tests stay offline.** Tests use local TLS servers and in-memory transports only, never Serper,
  never a real key, and never `t.Parallel`. No live test exists; one would need the owner's approval, a
  `live` build tag and an explicit environment switch, and would never run in CI.
- **Synthetic data only.** Fixtures are written from scratch with `example.com`, `example.org` and
  `example.net` values, and labeled as SDK fixtures; never copy provider examples or commit real
  responses.
