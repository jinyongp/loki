package cloudflare

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"

	"github.com/golang-jwt/jwt/v5"

	"loki/internal/auth"
)

type Access struct {
	TeamDomain string
	Audience   string
	JWKSPath   string
}

var _ auth.RequestVerifier = Access{}

func (a Access) VerifyRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	values := r.Header.Values("Cf-Access-Jwt-Assertion")
	return len(values) == 1 && a.Verify(values[0])
}

func (a Access) Verify(assertion string) bool {
	if len(assertion) > 32768 {
		return false
	}
	token, err := jwt.Parse(assertion, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodRS256 {
			return nil, errors.New("unexpected algorithm")
		}
		kid, ok := token.Header["kid"].(string)
		if !ok || kid == "" {
			return nil, errors.New("missing key id")
		}
		f, err := os.Open(a.JWKSPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 1_048_577))
		if err != nil || len(data) > 1_048_576 {
			return nil, errors.New("invalid JWKS")
		}
		var doc struct {
			Keys []struct {
				KID string `json:"kid"`
				KTY string `json:"kty"`
				Alg string `json:"alg"`
				Use string `json:"use"`
				N   string `json:"n"`
				E   string `json:"e"`
			} `json:"keys"`
		}
		if err = json.Unmarshal(data, &doc); err != nil {
			return nil, errors.New("invalid JWKS")
		}
		for _, key := range doc.Keys {
			if key.KID != kid {
				continue
			}
			if key.KTY != "RSA" || (key.Alg != "" && key.Alg != "RS256") || (key.Use != "" && key.Use != "sig") {
				return nil, errors.New("invalid signing key")
			}
			n, err := base64.RawURLEncoding.Strict().DecodeString(key.N)
			if err != nil {
				return nil, errors.New("invalid modulus")
			}
			e, err := base64.RawURLEncoding.Strict().DecodeString(key.E)
			if err != nil || len(e) > 4 {
				return nil, errors.New("invalid exponent")
			}
			exponent := new(big.Int).SetBytes(e).Int64()
			if exponent < 3 || exponent > 2147483647 || exponent%2 == 0 {
				return nil, errors.New("invalid exponent")
			}
			modulus := new(big.Int).SetBytes(n)
			if modulus.BitLen() < 2048 {
				return nil, errors.New("invalid modulus")
			}
			return &rsa.PublicKey{N: modulus, E: int(exponent)}, nil
		}
		return nil, errors.New("unknown signing key")
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer("https://"+a.TeamDomain), jwt.WithAudience(a.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	return err == nil && token != nil && token.Valid
}
