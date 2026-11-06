# ADR 0001: Modular monolith for the Go backend

Status: accepted

## Context
Ingestion, processing, querying, and alerting are separate concerns but share data and are developed by one person.

## Decision
One Go binary (with separate `cmd/` entry points for ingest, processor, and query so they can scale independently), organized as decoupled internal packages. The Python AI service is separate because it has a different runtime and scaling profile.

## Consequences
Simple local development and testing; clear seams to split later. Requires discipline to keep package boundaries clean.
