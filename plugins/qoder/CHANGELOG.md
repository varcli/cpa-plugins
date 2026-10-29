# Changelog

## 0.8.70

### CALL_COUNT_LIMIT queue metadata survives the nested envelope (PR #65, sabercomo)

Qoder can answer 10605 with the sub-code `CALL_COUNT_LIMIT` on the outer envelope while the actionable metadata (`retryAfterSeconds`, `modelKey`, `queueType`) rides the JSON-string child WITHOUT repeating `RESOURCE_LIMIT`. The queue parser recognized the outer code but only collected metadata from objects that themselves carried the identification — so a 17-second upstream hint fell back to the default 30-second wait, the model/queue fields vanished from logs, and the rendered line hard-coded `subCode=RESOURCE_LIMIT` with a "capacity limit, unrelated to the account" reading that a call-count ticket cannot support.

- `queue_retry.go`: the walk now threads an `inQueue` flag down the `message`/`details` carriers of an IDENTIFIED envelope, collecting sub-code and metadata from its descendants; an unrelated sibling hanging off an unidentified layer cannot overwrite the ticket. `queueLimitLine` renders the actual sub-code (omitting it when absent, never inventing one) and cause-neutral guidance instead of the account-unrelated claim.
- The `queue_limit` marker and the existing queue budget / wait cap / attempt limit / marker-keyed short (auth, model) cooldown are untouched.
- Tests: nested CALL_COUNT_LIMIT metadata through both carriers, unrelated-sibling isolation, no-invented-sub-code — all three red on 0.8.69, green here (discriminability verified during review).

## 0.8.69

### A oneOf of objects with no type gets type:object — kmodel 502 fix (magpie qoder 0.2.35, #378)

Absorbed from magpie-community/plugins `ebe3555` (qoder 0.2.35, 2026-10-25): Qoder CN's kmodel (Kimi K3.1 Pro) fails a chat whose tools hold a oneOf with no type — even when every branch is an object, directly or through a chain of local $refs (dsh's REA tools, a 502 here) — and takes the same request with `"type": "object"` beside each such oneOf (200 in a real dsh run). The same rule the workbuddy plugin absorbed from workbuddy 0.1.33.

- New `schema_typeunions.go`: `mendToolSchemasRaw` decodes the client's OpenAI tools payload, gives every untyped all-object oneOf inside each tool's parameters the type "object" (branches resolved through local `#/...` $ref chains, cycles broken, ≤16 hops; only schema keywords walked — SUB_ONE/SUB_LIST/SUB_MAP, depth ≤64), and re-encodes. Unions of other or mixed types, anyOf, and missing/external/circular $refs are left as they came. **Done on a decoded copy** — the caller's raw payload bytes are never touched (magpie's structuredClone discipline).
- Wired at the single tools-forwarding point in `buildQoderBody` (covers both execute and execute_stream paths); null / empty / non-array / nothing-to-mend payloads round-trip byte-for-byte.
- Tests (`schema_typeunions_test.go`): the issue's literal shapes — the minimal $ref → oneOf, the REA $ref-branch chain (with the lone-$ref-stays-untyped pin), the left-alone matrix (typed sibling / mixed / anyOf / missing / external / circular / examples — byte-for-byte round-trip), and the pass-through set. Full qoder suite green under `-race`.
