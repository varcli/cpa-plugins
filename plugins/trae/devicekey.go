// devicekey.go: EC P-256 device key pair for the ExchangeToken DeviceInfo.
//
// Why (v0.12.24): the official Trae client generates a FRESH P-256 key pair
// per login and uploads the SPKI public-key PEM inside
// DeviceInfo.DevicePublicKey ("每登录一套设备指纹：EC P-256 keypair + DeviceID +
// MachineID, 首登上传公钥、服务端绑定"). Sending an empty DevicePublicKey now
// surfaces as ExchangeToken => HTTP 401 business code 20405 (the 2xxxx family
// = credential/device-binding rejection; 10101 is the request-validation
// family, live-probed 2026-09-04).
//
// The private key is returned alongside so the login result can persist it
// (auth.devicePrivateKey) for the refresh DeviceProof signing the official
// client performs.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
)

// generateDeviceKeyPair creates a fresh P-256 key pair and returns the public
// key as a standard SPKI PEM ("-----BEGIN PUBLIC KEY-----") plus the private
// key as a PKCS#8 PEM ("-----BEGIN PRIVATE KEY-----").
func generateDeviceKeyPair() (publicKeyPEM, privateKeyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate P-256 key: %w", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("marshal PKCS#8 private key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal SPKI public key: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return string(pubPEM), string(privPEM), nil
}

// deviceBrandForContext maps the OS to a vendor brand for
// DeviceInfo.DeviceBrand while DeviceModel keeps the raw x_device_brand
// echoed from the login URL.
func deviceBrandForContext(deviceType string) string {
	switch deviceType {
	case "mac":
		return "Apple"
	case "windows":
		return "Microsoft"
	case "linux":
		return "Linux"
	default:
		return deviceType
	}
}
