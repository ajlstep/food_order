module food-order/bot

go 1.25.0

require (
	food-order/shared v0.0.0
	github.com/go-telegram/bot v1.27.0
	github.com/nats-io/nats.go v1.47.0
)

require (
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.42.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
)

replace food-order/shared => ../shared
