// Package profile is what an active person has set about themselves: name
// and language, settings, allergens, food preferences, saved addresses,
// phone, and the list of their channels.
//
// Same layers as package auth:
//
//	handler.go                         receives an rpc request, calls the service
//	service.go, food.go, locations.go  the rules
//	settings.go, norm.go               what values are allowed, in which form
//	store.go, store_sql.go             what the rules need from the database, and SQLite
//
// Every call is for one person, the one in the envelope of the request. The
// service checks that the account is active: the gateway already does, but
// the rule "nothing works before the email is confirmed" must not depend on
// every caller remembering it.
package profile
