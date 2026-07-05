package logging

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	// secretVisibleSuffix is how many trailing characters of a secret stay
	// visible (so an operator can correlate a masked value with their config).
	secretVisibleSuffix = 4
	// secretMaskPrefix is a fixed-width asterisk run used as the masked prefix.
	// Fixed width so the mask never reveals the real secret length.
	secretMaskPrefix = "************"
	// secretMinFullReveal: secrets at or below this length are masked entirely
	// (no visible suffix), so a short/low-entropy secret is not half-revealed.
	secretMinFullReveal = 8
	// secretMinRegister: secrets shorter than this are not redacted at all, to
	// avoid masking innocent common substrings and full-revealing tiny values.
	secretMinRegister = 6
)

// MaskSecret renders a secret as a fixed asterisk prefix plus a short visible
// suffix, e.g. "************wxyz". Secrets of length <= secretMinFullReveal are
// fully masked (no visible suffix); empty input returns "".
func MaskSecret(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= secretMinFullReveal {
		return secretMaskPrefix
	}
	return secretMaskPrefix + string(r[len(r)-secretVisibleSuffix:])
}

// secretForm is a concrete string to search for (a secret in raw or
// URL-query-encoded form) together with its precomputed masked replacement.
type secretForm struct {
	form   string
	masked string
}

// secretReplaceForms expands each secret into the concrete forms that may appear
// in a log line or error string (raw and URL-query-encoded), each paired with
// its MaskSecret rendering. Empty/too-short secrets are skipped. Forms are
// ordered longest-first so a shorter secret cannot partially mask a longer one.
func secretReplaceForms(secrets []string) []secretForm {
	seen := make(map[string]struct{})
	var forms []secretForm
	add := func(form, masked string) {
		if form == "" {
			return
		}
		if _, ok := seen[form]; ok {
			return
		}
		seen[form] = struct{}{}
		forms = append(forms, secretForm{form: form, masked: masked})
	}
	for _, sec := range secrets {
		sec = strings.TrimSpace(sec)
		if len([]rune(sec)) < secretMinRegister {
			continue
		}
		masked := MaskSecret(sec)
		add(sec, masked)
		// The same secret commonly appears URL-query-encoded inside *url.Error
		// strings (e.g. a Gotify token in "?token=..."); a verbatim replace of
		// the raw value would miss it, so cover the encoded form too.
		if enc := url.QueryEscape(sec); enc != sec {
			add(enc, masked)
		}
	}
	sort.Slice(forms, func(i, j int) bool {
		return len(forms[i].form) > len(forms[j].form)
	})
	return forms
}

// applySecretForms replaces every occurrence of each form in s with its mask.
func applySecretForms(s string, forms []secretForm) string {
	if s == "" {
		return s
	}
	for _, f := range forms {
		if strings.Contains(s, f.form) {
			s = strings.ReplaceAll(s, f.form, f.masked)
		}
	}
	return s
}

// RedactSecrets replaces every occurrence of each given secret (in raw and
// URL-query-encoded form) in s with its MaskSecret rendering. Use it to redact a
// known secret out of an error/log string at the source before wrapping/logging.
func RedactSecrets(s string, secrets ...string) string {
	if s == "" || len(secrets) == 0 {
		return s
	}
	return applySecretForms(s, secretReplaceForms(secrets))
}

// RedactURLError strips the URL from a *url.Error so it never reaches a log or a
// caller: request URLs to the central servers embed low-capability secrets in the
// path/query (a check UUID, or the server_id). net's *url.Error.Error() prints the
// full URL verbatim; this keeps only the operation + underlying transport error.
// Non-*url.Error values pass through unchanged.
func RedactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
