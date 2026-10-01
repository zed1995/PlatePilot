// Package store declares the repository and provider interfaces the two
// services depend on, with the PostgreSQL and in-memory implementations under
// the subdirectories of this package tree. Vendor types (pgx, Ollama, OpenAI)
// must never leak across these interfaces into the domain or application
// layers.
package store
