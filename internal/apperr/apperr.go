// Package apperr defines stable, machine-readable error codes for
// user-facing errors. The backend has no localization layer, so it emits a
// marker the frontend recognizes:
//
//	[svc:<code>]{"param":"value",...}
//
// The frontend (utils/error.ts) matches the marker and renders a translated
// message from its i18n catalog; unknown codes fall back to the raw text.
// Wrapped low-level causes (network, filesystem) stay plain English — their
// detail is dynamic and only useful for diagnostics.
package apperr

import (
	"encoding/json"
	"fmt"
)

// Code identifies a user-facing error class. Values become the i18n key
// suffix on the frontend ("errors.<code>"), so keep them short kebab-case.
type Code string

const (
	AppNotInitialized     Code = "app-not-initialized"
	UnknownSdkType        Code = "unknown-sdk-type"
	PathNotExist          Code = "path-not-exist"
	NotInPath             Code = "not-in-path"
	HiddenCopy            Code = "hidden-copy"
	ManagedCopy           Code = "managed-copy"
	ProtectedImport       Code = "protected-import"
	ProtectedDir          Code = "protected-dir"
	ManagedDir            Code = "managed-dir"
	JdkRequired           Code = "jdk-required"
	SdkIncomplete         Code = "sdk-incomplete"
	ExecNotFound          Code = "exec-not-found"
	ExecTimeout           Code = "exec-timeout"
	ExecFailed            Code = "exec-failed"
	VersionParseFail      Code = "version-parse-fail"
	VersionMismatch       Code = "version-mismatch"
	ChecksumMismatch      Code = "checksum-mismatch"
	VersionDirMissing     Code = "version-dir-missing"
	NoBackup              Code = "no-backup"
	NoAsset               Code = "no-asset"
	UpdateHttpStatus      Code = "update-http-status"
	TargetExists          Code = "target-exists"
	NestedDirs            Code = "nested-dirs"
	PathNotAbsolute       Code = "path-not-absolute"
	SystemDir             Code = "system-dir"
	NeedSdk               Code = "need-sdk"
	ComposerManual        Code = "composer-manual"
	UnknownPackageManager Code = "unknown-package-manager"
	PrivateIp             Code = "private-ip"
	HttpStatus            Code = "http-status"
	SchemeNotAllowed      Code = "scheme-not-allowed"
)

// New builds the marked error. params may be nil; values are interpolated
// into the translated message on the frontend. JSON keys are marshaled in
// sorted order, so identical calls produce identical text (testable).
func New(code Code, params map[string]string) error {
	if len(params) == 0 {
		return fmt.Errorf("[svc:%s]", string(code))
	}
	b, err := json.Marshal(params)
	if err != nil {
		// Unreachable for map[string]string, but never lose the code.
		return fmt.Errorf("[svc:%s]", string(code))
	}
	return fmt.Errorf("[svc:%s]%s", string(code), b)
}
