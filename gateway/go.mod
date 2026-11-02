module food-order/gateway

go 1.25.0

require (
	food-order/shared v0.0.0
	github.com/nats-io/nats-server/v2 v2.11.9
	github.com/nats-io/nats.go v1.47.0
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.8.0-default-no-op // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/minio/highwayhash v1.0.4 // indirect
	github.com/nats-io/jwt/v2 v2.8.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

replace food-order/shared => ../shared
