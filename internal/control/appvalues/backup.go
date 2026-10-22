package appvalues

// ApplicationExportProof describes an operator-encrypted logical app artifact.
// CipherSHA256 authenticates ciphertext, never an application value or pattern.
// Its fixed artifact location is derived by the host from operation/resource.
type ApplicationExportProof struct {
	Version      int    `json:"version"`
	Operation    string `json:"operation_id"`
	Resource     string `json:"resource_id"`
	Policy       string `json:"policy"`
	Revision     string `json:"revision"`
	Recipient    string `json:"recipient"`
	CipherSHA256 string `json:"cipher_sha256"`
	Bytes        int    `json:"bytes"`
}
