# ADR-0007: Bind Model Roles, Credentials and Consent

- Status: Accepted

## Context
Model output is untrusted; endpoint configuration and credentials must not become
content, fallback routing or delegated execution authority.

## Decision
Support only OpenAI through native Eino Chat Completions and Responses.
Configuration has one required models.agent slot and optional
models.approval_reviewer slot. The slot determines role, identity and credential
binding. Each profile supplies its own canonical origin, model, non-secret
settings, finite limits and opaque credential. No inheritance, provider selector,
routing, probing, fallback, cross-origin retry or prompt-parsed Tools is admitted.

api_protocol is explicit. Optional temperature preserves omission and explicit
zero; reasoning effort is never automatically disabled. Role and protocol derive
streaming and Tool availability. Null and unknown keys are rejected. Retain exact
request tests for native component behavior rather than guessing model support
from a name.

Consent binds policy version, role, canonical-origin hash and exact data
categories. Relevant changes invalidate consent and pending authority before
further content requests. HTTPS verifies certificates; HTTP is loopback-only.
Reject userinfo, query, insecure TLS and cross-origin Authorization forwarding.

Credentials use an opaque non-renderable wrapper. Admit only role-specific
environment input, masked setup input and disclosed plaintext Home storage.
Do not put credentials in CLI arguments, ordinary Config, Domain, child
environments, errors, logs, prompts, history, SQLite or exports.

The optional Reviewer receives a minimal safe intent/envelope/policy projection,
makes one strict non-streaming Tool-free call, and returns approve, deny or
escalate_to_user with bounded rationale. It is not authority. Malformed output,
Tool calls, timeout, missing consent, stale state or transport failure authorizes
nothing. Only deterministic permission routing can admit a valid recommendation.

## Consequences and validation
Explicit profiles avoid hidden credential sharing and endpoint negotiation.
Use safe typed failure classes while retaining internal causes; raw endpoint
error strings never control production policy. Exact HTTP fixtures verify request
settings, origin, authorization, closure, budgets and zero-call denials.
See [Configuration](../configuration.md), [Model Compatibility](../model-compatibility.md)
and [Privacy](../privacy-overview.md).
