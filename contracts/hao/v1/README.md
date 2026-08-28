# `hao` v1 contract baseline

This directory freezes the machine-readable inputs consumed by later `hao`
batches. It is contract data, not an implementation of setup, initialization,
service mutation, or pairing.

| Contract | Artifact | Compatibility rule |
| --- | --- | --- |
| Non-secret configuration | `config.schema.json`, `examples/` | `version: 1` is required. Unknown keys and unknown schema versions fail closed. Runtime implementation is deliberately absent. |
| CLI failures | `errors.json` | Exit statuses and symbolic codes are stable within v1. JSON errors always carry every field named by `errorEnvelope.required`. |
| Components | `compatibility.json` | Compatibility is evaluated per interface, not inferred from one shared product version. Unknown interface versions are unsupported. |
| Legacy installations | `legacy/manifest.json`, `legacy/service-shapes.json`, `legacy/machine.json` | Detection must recognize these shapes without changing or deleting them. The fixtures contain placeholders, never credentials. |
| Pairing string | [`backend/internal/pairstring/vectors.json`](../../../backend/internal/pairstring/vectors.json) | This existing file remains the single Go/TypeScript golden-vector source. Do not copy it here. |
| Remote route policy | `gateway-route-policy.json` | The gateway is deny-by-default. The fixture is checked against the current Go policy. |

## Component compatibility

The compatibility model intentionally avoids promising that equal package
versions are interchangeable. The components have independently meaningful
interfaces:

| Consumer | Provider | Interface | Supported in this baseline |
| --- | --- | --- | --- |
| `hao` | config file | `hao-config` | exactly v1 |
| `hao` | AO daemon | daemon application/doctor API | capability negotiation; an unknown API contract is reported unsupported |
| gateway | AO daemon | loopback application API and mux | the compatibility-pinned release bundle; route policy v1 remains authoritative |
| desktop | gateway | REST/SSE/mux transport | route policy v1 plus the selected authentication mode |
| desktop / `hao` | pair string | `ao-pair` | exactly v1 |
| `hao` adoption | legacy services/state | named shapes in `legacy/manifest.json` | recognize and preserve; mutation is outside Batch 1 |

The artifact that will package `hao`, the daemon, and gateway is intentionally
not selected here; that remains a product decision in the architecture spec.

## Validation

Focused validation is part of the Go tests:

```sh
cd backend
go test ./internal/haocontract ./internal/pairstring ./internal/vmgateway ./internal/cli
```

These tests validate the examples and JSON contracts, exercise the shared pair
vectors in Go, verify the same vectors are imported by the frontend tests, and
compare the route/service fixtures with current runtime truth.
