package main

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// failCode writes an API error the page can show in the owner's language
// (ADR 0013): code is a stable id the page translates (web/i18n.js has
// error.<code>), params fill its {name} slots, and message is the Japanese
// text shown by pages that do not know the code. Codes are lowercase
// snake_case; TestErrorCodesHaveStrings checks that each one has a string.
// Params are strings, numbers, or lists ([]string, joined by the page in
// its language). They must hold nothing secret: they reach the page as they are.
func failCode(w http.ResponseWriter, status int, code string, params map[string]any, message string) {
	body := map[string]any{"error": message, "code": code}
	if len(params) > 0 {
		body["params"] = params
	}
	jsonResponse(w, status, body)
}

// failCodeWith is failCode with more fields the page reads (the
// gateway's refusal: its pairs and raw words).
func failCodeWith(w http.ResponseWriter, status int, code string, params map[string]any, message string, extra map[string]any) {
	body := map[string]any{"error": message, "code": code}
	if len(params) > 0 {
		body["params"] = params
	}
	for k, v := range extra {
		body[k] = v
	}
	jsonResponse(w, status, body)
}

// rejectCode is reject (server.go) with a code: it also explains the
// rejection in the log. Never pass tokens, cookies, or CSRF values to it.
func rejectCode(w http.ResponseWriter, r *http.Request, status int, code, message, reason string) {
	log.Printf("rejected %s %s: %s (%s)", r.Method, r.URL.Path, code, reason)
	failCode(w, status, code, nil, message)
}

// codedError is an error the page can show in the owner's language: failErr
// sends its code and params as failCode does, with the Japanese text. en is
// its English text for the console and the log (english).
type codedError struct {
	code   string
	params map[string]any
	msg    string
	en     string
	cause  error
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Unwrap() error { return e.cause }

// errCode makes a codedError (code and params as failCode's).
func errCode(code string, params map[string]any, msg string) error {
	return &codedError{code: code, params: params, msg: msg}
}

// errCodeEn is errCode with the English text, and the error it wraps
// (errors.Is and As see it; may be nil).
func errCodeEn(code string, params map[string]any, msg, en string, cause error) error {
	return &codedError{code: code, params: params, msg: msg, en: en, cause: cause}
}

// english is err's text for the console and the log: each codedError's
// Japanese text becomes its English one, or its code and params.
func english(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for e := err; e != nil; e = errors.Unwrap(e) {
		c, ok := e.(*codedError)
		if !ok || c.msg == "" {
			continue
		}
		en := c.en
		if en == "" {
			en = c.code
			if len(c.params) > 0 {
				var kv []string
				for _, k := range slices.Sorted(maps.Keys(c.params)) {
					kv = append(kv, fmt.Sprintf("%s=%v", k, c.params[k]))
				}
				en += " (" + strings.Join(kv, ", ") + ")"
			}
		}
		s = strings.Replace(s, c.msg, en, 1)
	}
	return s
}

// failErr writes err as an API error: with its code when it is a
// codedError as it is (a wrapped one says more than its code does).
func failErr(w http.ResponseWriter, status int, err error) {
	var c *codedError
	if errors.As(err, &c) && c.msg == err.Error() {
		failCode(w, status, c.code, c.params, c.msg)
		return
	}
	fail(w, status, err.Error())
}
