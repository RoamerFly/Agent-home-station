//go:build windows

package nativeclient

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Signature struct {
	State             string `json:"state"`
	Publisher         string `json:"publisher,omitempty"`
	Organization      string `json:"organization,omitempty"`
	CertificateSHA256 string `json:"certificate_sha256,omitempty"`
}

var (
	wintrust        = windows.NewLazySystemDLL("wintrust.dll")
	trustProvider   = wintrust.NewProc("WTHelperProvDataFromStateData")
	trustSigner     = wintrust.NewProc("WTHelperGetProvSignerFromChain")
	trustSignerCert = wintrust.NewProc("WTHelperGetProvCertFromChain")
)

func inspectSignature(path *uint16, handle windows.Handle) Signature {
	file := windows.WinTrustFileInfo{Size: uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})), FilePath: path, File: handle}
	policy := windows.WINTRUST_ACTION_GENERIC_VERIFY_V2
	trust := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_WHOLECHAIN,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&file),
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		ProvFlags:                       windows.WTD_CACHE_ONLY_URL_RETRIEVAL | windows.WTD_REVOCATION_CHECK_CHAIN_EXCLUDE_ROOT | windows.WTD_DISABLE_MD2_MD4,
	}
	defer func() {
		trust.StateAction = windows.WTD_STATEACTION_CLOSE
		_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &policy, &trust)
	}()
	if err := windows.WinVerifyTrustEx(windows.InvalidHWND, &policy, &trust); err != nil {
		return Signature{State: signatureFailure(err)}
	}
	// Read only the leaf of the exact signer WinTrust verified. An embedded
	// certificate or the CompanyName resource alone is not signature evidence.
	cert := verifiedLeaf(trust.StateData)
	if cert == nil || cert.EncodedCert == nil || cert.Length == 0 || cert.Length > 64<<10 {
		return Signature{State: "inconclusive"}
	}
	der := unsafe.Slice(cert.EncodedCert, int(cert.Length))
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		return Signature{State: "inconclusive"}
	}
	sum := sha256.Sum256(der)
	result := Signature{State: "trusted", Publisher: parsed.Subject.CommonName, CertificateSHA256: hex.EncodeToString(sum[:])}
	if len(parsed.Subject.Organization) == 1 {
		result.Organization = parsed.Subject.Organization[0]
	}
	return result
}

func signatureFailure(err error) string {
	switch {
	case errors.Is(err, syscall.Errno(windows.TRUST_E_NOSIGNATURE)):
		return "unsigned"
	case errors.Is(err, syscall.Errno(windows.TRUST_E_BAD_DIGEST)),
		errors.Is(err, syscall.Errno(windows.TRUST_E_EXPLICIT_DISTRUST)),
		errors.Is(err, syscall.Errno(windows.CERT_E_REVOKED)),
		errors.Is(err, syscall.Errno(windows.CERT_E_EXPIRED)),
		errors.Is(err, syscall.Errno(windows.CERT_E_UNTRUSTEDROOT)):
		return "untrusted"
	default:
		return "inconclusive"
	}
}

func verifiedLeaf(state windows.Handle) *windows.CertContext {
	if state == 0 || trustProvider.Find() != nil || trustSigner.Find() != nil || trustSignerCert.Find() != nil {
		return nil
	}
	provider, _, _ := trustProvider.Call(uintptr(state))
	if provider == 0 {
		return nil
	}
	signer, _, _ := trustSigner.Call(provider, 0, 0, 0)
	if signer == 0 {
		return nil
	}
	address, _, _ := trustSignerCert.Call(signer, 0)
	if address == 0 {
		return nil
	}
	// WinTrust owns the pointer until WTD_STATEACTION_CLOSE.
	type providerCertificate struct {
		Size        uint32
		Certificate *windows.CertContext
	}
	cert := *(**providerCertificate)(unsafe.Pointer(&address))
	if uintptr(cert.Size) < unsafe.Sizeof(providerCertificate{}) {
		return nil
	}
	return cert.Certificate
}
