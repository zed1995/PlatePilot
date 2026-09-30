// Package port declares the interfaces that the domain and application layers
// depend on. Implementations live under internal/adapter and must never leak
// vendor types (pgx, Eino, Hertz, Ollama, OpenAI) back across this boundary.
package port
