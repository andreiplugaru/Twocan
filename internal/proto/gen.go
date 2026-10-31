// Package pb holds the generated protobuf types for Twocan's wire protocols.
package pb

//go:generate protoc --go_out=. --go_opt=paths=source_relative addrs.proto card.proto chat.proto contact.proto logs.proto
