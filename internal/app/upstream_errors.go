package app

import (
	"errors"
	"net/http"
	"strings"
)

// upstreamErrorClass is the OpenAI-compatible classification of an upstream
// failure. Phase 1 (P0-4): quota and rate-limit conditions must not be buried
// as a generic 502, otherwise SDKs cannot back off correctly.
type upstreamErrorClass struct {
	Status int
	Type   string
	Code   string
}

func insufficientQuotaClass() upstreamErrorClass {
	return upstreamErrorClass{
		Status: http.StatusTooManyRequests,
		Type:   "insufficient_quota",
		Code:   "insufficient_quota",
	}
}

func rateLimitClass() upstreamErrorClass {
	return upstreamErrorClass{
		Status: http.StatusTooManyRequests,
		Type:   "rate_limit_error",
		Code:   "rate_limit_exceeded",
	}
}

func authClass() upstreamErrorClass {
	return upstreamErrorClass{
		Status: http.StatusUnauthorized,
		Type:   "invalid_request_error",
		Code:   "invalid_api_key",
	}
}

func upstreamUnavailableClass() upstreamErrorClass {
	return upstreamErrorClass{
		Status: http.StatusServiceUnavailable,
		Type:   "api_error",
		Code:   "upstream_unavailable",
	}
}

func upstreamTimeoutClass() upstreamErrorClass {
	return upstreamErrorClass{
		Status: http.StatusGatewayTimeout,
		Type:   "api_timeout_error",
		Code:   "upstream_timeout",
	}
}

func classifyUpstreamError(err error) upstreamErrorClass {
	if err == nil {
		return upstreamErrorClass{Status: http.StatusBadGateway, Type: "api_error", Code: "upstream_error"}
	}
	var apiErr *notionAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		if class, ok := classifyUpstreamStatus(apiErr.StatusCode); ok {
			return class
		}
	}
	var stepErr *inferenceStepError
	if errors.As(err, &stepErr) && stepErr != nil {
		if class, ok := classifyInferenceSubType(stepErr.SubType); ok {
			return class
		}
	}
	if class, ok := classifyUpstreamMessage(strings.ToLower(strings.TrimSpace(err.Error()))); ok {
		return class
	}
	if IsQuotaExhaustedError(err) {
		return insufficientQuotaClass()
	}
	if errors.Is(err, errAccountStarved) {
		return upstreamUnavailableClass()
	}
	return upstreamErrorClass{Status: http.StatusBadGateway, Type: "api_error", Code: "upstream_error"}
}

func classifyUpstreamStatus(status int) (upstreamErrorClass, bool) {
	switch status {
	case http.StatusPaymentRequired:
		return insufficientQuotaClass(), true
	case http.StatusTooManyRequests:
		return rateLimitClass(), true
	case http.StatusUnauthorized, http.StatusForbidden:
		return authClass(), true
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return upstreamUnavailableClass(), true
	}
	return upstreamErrorClass{}, false
}

func classifyInferenceSubType(subType string) (upstreamErrorClass, bool) {
	switch strings.ToLower(strings.TrimSpace(subType)) {
	case "quota-exhausted", "quota_exhausted", "premium-feature-unavailable", "premium_feature_unavailable", "insufficient-quota":
		return insufficientQuotaClass(), true
	case "rate-limited", "rate_limited", "too-many-requests", "too_many_requests":
		return rateLimitClass(), true
	case "temporarily-unavailable", "temporarily_unavailable", "service-unavailable":
		return upstreamUnavailableClass(), true
	case "unauthorized", "forbidden", "session-expired", "session_expired":
		return authClass(), true
	}
	return upstreamErrorClass{}, false
}

func classifyUpstreamMessage(message string) (upstreamErrorClass, bool) {
	switch {
	case containsAny(message,
		"premium-feature-unavailable", "premium feature", "insufficient_quota", "insufficient quota",
		"payment required", "quota-exhausted", "quota exhausted", "exceeded your current quota", "failed: 402"):
		return insufficientQuotaClass(), true
	case containsAny(message, "timeout", "deadline exceeded"):
		return upstreamTimeoutClass(), true
	case containsAny(message, "429", "rate limit", "too many requests", "rate-limited"):
		return rateLimitClass(), true
	case containsAny(message, "unauthorized", "forbidden", "invalid_or_expired", "session expired", "failed: 401", "failed: 403"):
		return authClass(), true
	case containsAny(message, "temporarily-unavailable", "temporarily unavailable", "service unavailable", "bad gateway", "failed: 503", "failed: 502"):
		return upstreamUnavailableClass(), true
	}
	return upstreamErrorClass{}, false
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
