package access

import "context"

// Identity is who the person is, as the channel knows them.
type Identity struct {
	Channel    string // telegram
	ExternalID string // the person's id in the channel
}

// Person is the account behind an Identity, as users answered.
type Person struct {
	UserID   string
	Status   string // pending | active (the gate lets no other through)
	Language string
}

type identityKey struct{}
type personKey struct{}

func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

func withPerson(ctx context.Context, p Person) context.Context {
	return context.WithValue(ctx, personKey{}, p)
}

// IdentityFrom returns the identity of the request (empty before access).
func IdentityFrom(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey{}).(Identity)
	return id
}

// PersonFrom returns the account of the request (empty on LevelChannel routes).
func PersonFrom(ctx context.Context) Person {
	p, _ := ctx.Value(personKey{}).(Person)
	return p
}
