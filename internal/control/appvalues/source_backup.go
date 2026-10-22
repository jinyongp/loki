package appvalues

// SourceBackupProof binds the original encrypted legacy recovery artifact.
// The digest covers ciphertext only; no key or source value appears here.
type SourceBackupProof struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation"`
	Revision     uint64 `json:"source_revision"`
	Recipient    string `json:"recipient"`
	CipherSHA256 string `json:"cipher_sha256"`
	Bytes        int64  `json:"bytes"`
}
