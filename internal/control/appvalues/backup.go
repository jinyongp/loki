package appvalues

import (
	"encoding/binary"
	"errors"
)

const MaxApplicationArchive = 64 << 20
const MaxApplicationRestoreInput = MaxApplicationArchive + 65536 + 4

// Restore input contains only operator identity bytes and an encrypted logical
// profile. It travels on the root-only private byte channel, never JSON/argv.
func EncodeApplicationRestore(identity, archive []byte) ([]byte, error) {
	if len(identity) < 1 || len(identity) > 65536 || len(archive) < 1 || len(archive) > MaxApplicationArchive {
		return nil, errors.New("invalid private restore input")
	}
	body := make([]byte, 4+len(identity)+len(archive))
	binary.BigEndian.PutUint32(body, uint32(len(identity)))
	copy(body[4:], identity)
	copy(body[4+len(identity):], archive)
	return body, nil
}

func DecodeApplicationRestore(body []byte) (identity, archive []byte, err error) {
	if len(body) < 6 || len(body) > MaxApplicationRestoreInput {
		return nil, nil, errors.New("invalid private restore input")
	}
	n := int(binary.BigEndian.Uint32(body[:4]))
	if n < 1 || n > 65536 || n >= len(body)-4 || len(body)-4-n > MaxApplicationArchive {
		return nil, nil, errors.New("invalid private restore input")
	}
	return body[4 : 4+n], body[4+n:], nil
}

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
