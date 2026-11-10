// Package incident holds the incident model of ADR 0026: the pure
// correlation that decides which incident a firing alert joins (Correlate),
// the flapping rule (Collapse), the status workflow (Transition) and the
// closed timeline event schemas. It has no storage and no clock: callers
// pass the time and the candidates.
package incident
