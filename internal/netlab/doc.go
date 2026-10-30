// Package netlab runs real Twocan binaries on simulated networks: a helper
// on a public network, a desktop and a phone behind NAT routers, and the
// phone switching between Wi-Fi and mobile data. It is only tests, built
// with the netlab tag:
//
//	go test -tags netlab -v -timeout 30m ./internal/netlab
//
// The networks are Linux network namespaces inside an unprivileged user
// namespace, so no root is needed; it needs ip, nft and tc.
package netlab
