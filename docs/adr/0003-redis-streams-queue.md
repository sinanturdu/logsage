# ADR 0003: Redis Streams between ingestion and storage

Status: accepted

## Context
The ingestion API must respond fast and survive bursts; storage writes are batched.

## Decision
Use Redis Streams with consumer groups. Ingestion appends; the processor and alert engine consume independently and acknowledge after success.

## Consequences
Decoupling, backpressure, replay of unacknowledged messages. Redis is a durability limit (configure AOF); Kafka is a possible later replacement behind the same interface.
