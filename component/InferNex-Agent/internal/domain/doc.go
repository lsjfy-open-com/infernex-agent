// Package domain owns the pd-json-v1 private-deployment record contract.
//
// Adapters build an Environment or InventorySnapshot from typed values and call
// FinalizeRecord. Persistence and API boundaries call DecodeRecord or
// VerifyRecord; they must not normalize records while reading them. Record and
// entity references always carry their full tenant and immutable revision
// context.
package domain
