package adroute

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

type AppError struct {
	Message string
	Status  int
	Code    string
}

func (e *AppError) Error() string { return e.Message }
func appError(message string, status int, code string) *AppError {
	return &AppError{message, status, code}
}

var bearerRE = regexp.MustCompile(`(?i)Bearer\s+[^\s"'<>]+`)

func Redact(text string, secrets []string) string {
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "[REDACTED]")
		}
	}
	return bearerRE.ReplaceAllString(text, "Bearer [REDACTED]")
}
func ErrorStatus(err error) int {
	var e *AppError
	if errors.As(err, &e) {
		return e.Status
	}
	return 500
}

func ErrorPayload(err error, secrets []string) map[string]any {
	var e *AppError
	if errors.As(err, &e) && e.Status >= 400 && e.Status < 600 {
		code := e.Code
		if code == "" {
			code = "REQUEST_FAILED"
		}
		return map[string]any{"error": map[string]string{"code": code, "message": Redact(e.Message, secrets)}}
	}
	return map[string]any{"error": map[string]string{"code": "INTERNAL_ERROR", "message": "服务内部错误，请查看服务状态后重试。"}}
}
