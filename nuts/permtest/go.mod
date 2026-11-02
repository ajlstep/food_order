module food-order/nuts-permtest

go 1.25.0

require (
	food-order/shared v0.0.0
	github.com/nats-io/nats-server/v2 v2.11.9
	github.com/nats-io/nats.go v1.47.0
)

replace food-order/shared => ../../shared
