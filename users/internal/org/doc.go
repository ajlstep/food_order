// Package org is companies, groups and invitations.
//
// Same layers as packages auth and profile:
//
//	handler.go              receives an rpc request, calls the service
//	service.go              what is common: limits, who is active, events
//	companies.go, groups.go,
//	invites.go              the rules
//	store.go, store_sql.go  what the rules need from the database, and SQLite
//
// A group is only a common address. Nobody sees what somebody else ordered
// through it; only the owner of a company sees the orders made at the
// addresses of the company (the report is made in core).
//
// Events go out after the transaction is saved: a person must not get a
// message about something that was undone. If an event is lost, nothing is
// wrong in the data: an invitation is still in /invitatii.
package org
