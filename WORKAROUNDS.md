# Workarounds

## sqlite-vec musl typedef aliases

- What: `Dockerfile.portable` builds the static Linux binary with `CGO_CFLAGS="-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t"` while compiling sqlite-vec.
- Why: `github.com/asg017/sqlite-vec-go-bindings` v0.1.6 references BSD `u_int*` aliases in its bundled C source. musl does not define those aliases, so the static Alpine/musl build fails even though the same code builds on glibc.
- Proper fix: Upgrade sqlite-vec/go bindings once they avoid BSD-only typedef assumptions, or carry a small upstreamable patch in the dependency instead of passing preprocessor aliases from the build.

## Spring REST UDS HTTP/1.1 Forcing

- What: `java/spring/memory-service-rest-spring` uses a custom `UnixDomainSocketClientHttpConnector` and reflectively forces Reactor Netty's outbound request to `HTTP/1.1` for Unix-domain-socket REST calls.
- Why: With direct UDS transport, Reactor Netty was emitting `HTTP/3.0` request versions on the socket, and the Go memory-service listener rejected them with `505 HTTP Version Not Supported`.
- Proper fix: Replace the reflective override with a supported Reactor Netty/Spring WebFlux configuration path that guarantees `HTTP/1.1` over UDS, or move Spring REST UDS to a client stack that exposes first-class Unix-socket + HTTP-version control without reflection.

## GitHub Cucumber Annotation Timeout

- What: The optional `deblockt/cucumber-report-annotations-action@v1.21` CI step has a 5-minute timeout while keeping `continue-on-error: true`.
- Why: The action can hang after test execution, artifact upload, and generated Cucumber report files have already succeeded, leaving an otherwise complete matrix job stuck in progress.
- Proper fix: Replace the third-party annotation action with a maintained reporting path or an in-repo summary generator that cannot block required CI jobs indefinitely.

## Chat client nullable title type bridge

- What: The chat frontend casts the nullable conversation-title update to the generated string type when clearing a title.
- Why: The OpenAPI document declares version 3.1 but expresses nullability with the OpenAPI 3.0-only `nullable: true` keyword. `@hey-api/openapi-ts` 0.97.3 ignores that keyword for 3.1 input and generates `title?: string`, although the server contract and behavior accept `null`.
- Proper fix: Convert nullable schemas in the OpenAPI contract to valid 3.1 unions that include `null`, regenerate every client, and remove the cast.

## MongoDB InWriteTx is intent-only (non-transactional)

- What: `MongoStore.InWriteTx` sets a txscope intent flag on the context but does NOT open a MongoDB multi-document session transaction. Multiple store writes inside a single `InWriteTx` call are not wrapped in a real atomic transaction.
- Why: Implementing real MongoDB session transactions would require refactoring the store to thread a `mongo.Session` through all write paths, including `AppendEntries`, `SyncAgentEntry`, attachment linking, and outbox event appends. This was deferred to avoid scope creep in the initial port.
- Impact: The inline `conversationPatch` on `AppendEntries`/`SyncAgentEntry` is **not** truly atomic on MongoDB — the entry write and the subsequent conversation patch (title/metadata/archive) are separate operations. A failure between them will leave the database in a partially-updated state. Postgres and SQLite are genuinely atomic (GORM transactions). Mongo outbox appends are also best-effort (see the `Mongo outbox staging rule` in `internal/FACTS.md`).
- Proper fix: Upgrade `MongoStore.InWriteTx` to use `mongo.Client.StartSession` + `session.WithTransaction`, threading the session context through all write operations. Then verify that all collections used in a single transaction are within the same replica set shard to avoid cross-shard transaction limitations.
