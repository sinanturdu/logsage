# ADR 0004: Go for the backend

Status: accepted

## Context
The core workload is high-throughput, concurrent data handling with low memory overhead.

## Decision
Implement the backend in Go. The owner chose Go for this project.

## Consequences
Strong concurrency and a single static binary; the AI service stays in Python where the ML ecosystem is.
