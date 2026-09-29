// Package memory provides in-memory implementations of the port Repository
// interfaces. They are used for local development and tests, and they act as the
// behavioural reference that the Mongo adapter (M1) must match: missing records
// return a not_found error and repeated upserts stay idempotent.
package memory
