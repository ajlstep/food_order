package auth

import "food-order/users/internal/refusal"

// Error and fail are the refusals of the rules; see package refusal.
type Error = refusal.Error

var fail = refusal.New
