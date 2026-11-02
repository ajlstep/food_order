module food-order/users

go 1.25.0

require (
	food-order/shared v0.0.0
	github.com/google/uuid v1.6.0
	github.com/nats-io/nats-server/v2 v2.11.9
	github.com/nats-io/nats.go v1.47.0
	github.com/ncruces/go-sqlite3 v0.29.1
)

require (
	github.com/antithesishq/antithesis-sdk-go v0.4.3-default-no-op // indirect
	github.com/google/go-tpm v0.9.5 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/minio/highwayhash v1.0.3 // indirect
	github.com/nats-io/jwt/v2 v2.7.4 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/tetratelabs/wazero v1.9.0 // indirect
	golang.org/x/crypto v0.42.0 // indirect
	golang.org/x/sys v0.36.0 // indirect
	golang.org/x/time v0.13.0 // indirect
)

replace food-order/shared => ../shared
