// Package repository defines the storage interfaces the handlers depend on.
// AR-1 ships no business logic, so this is deliberately empty beyond the
// aggregate struct — AR-2/AR-3 add one interface per entity here (AppRepository,
// ArtifactRepository, ...) following the manmanv2/api/repository pattern, and
// postgres/ gains the pgx-backed implementation for each.
package repository

// Repository aggregates the per-entity repository interfaces. Empty in AR-1;
// fields are added alongside the RPCs that need them.
type Repository struct{}
