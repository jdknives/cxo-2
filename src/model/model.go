package model

import (
	"fmt"
	"time"
)

// RootHash model
type RootHash struct {
	Publisher string `json:"publisher"`
	Signature string `json:"signature"`
	Sequence  uint64 `json:"sequence"`
}

// Returns data object key constructed in "sequence:publisher" format
func (r *RootHash) Key() string {
	return fmt.Sprintf("%v:%s", r.Sequence, r.Publisher)
}

// DataObject model
type DataObject struct {
	Header   Header   `json:"header"`
	Manifest Manifest `json:"manifest"`
	Objects  []Object `json:"object"`
}

// Header model
type Header struct {
	Timestamp    time.Time `json:"timestamp"`
	ManifestHash string    `json:"manifestHash"`
	ManifestSize uint64    `json:"manifestSize"`
	DataHash     string    `json:"dataHash"`
	DataSize     uint64    `json:"dataSize"`
}

// Manifest model
type Manifest struct {
	Length uint64            `json:"length"`
	Hashes []ObjectStructure `json:"objects"`
	Meta   []string          `json:"meta"`
}

// ObjectStructure model
type ObjectStructure struct {
	Index                   uint64 `json:"index"`
	Hash                    string `json:"hash"`
	Size                    uint64 `json:"size"`
	RecursiveSizeFirstLevel uint64 `json:"recursiveSizeFirstLevel"`
	RecursiveSizeFirstTotal uint64 `json:"recursiveSizeTotal"`
}

// Object model
type Object struct {
	Length uint64 `json:"length"`
	Data   []byte `json:"data"`
}

// AnnounceDataRequest model
type AnnounceDataRequest struct {
	RootHash   RootHash   `json:"rootHash"`
	DataObject DataObject `json:"dataObject"`
}
