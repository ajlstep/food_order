// Package auth is the "who is this person" part of users: accounts, the
// channels they write from, and the confirmation of their email.
//
// Layers (each one talks only to the one below):
//
//	handler.go             receives an rpc request, checks its shape, calls a service
//	resolver.go, email.go  the rules (Resolver: who is this; EmailService: the code)
//	store.go               the interfaces the rules need; store_sql.go implements them
//
// Rules that depend on time (a code works 30 minutes) take the clock from the
// constructor, so tests do not have to wait.
package auth
