package einoadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const safeProviderErrorBody = `{"error":{"message":"The model endpoint rejected the request."}}`

type transportRequestStateKey struct{}

type transportRequestState struct {
	mu                         sync.Mutex
	sensitiveDiagnostics       bool
	enteredTransport           bool
	providerErrorBody          string
	providerErrorBodyTruncated bool
	httpStatus                 int
	responseBody               *boundedSSEBody
}

func (state *transportRequestState) markTransportEntered() {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.enteredTransport = true
}

func (state *transportRequestState) transportEntered() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.enteredTransport
}

func (state *transportRequestState) setHTTPStatus(value int) {
	if value < 100 || value > 599 {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.httpStatus = value
}

func (state *transportRequestState) status() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.httpStatus
}

func (state *transportRequestState) setProviderError(body []byte, truncated bool) {
	if !state.sensitiveDiagnostics || len(body) == 0 {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.providerErrorBody = string(body)
	state.providerErrorBodyTruncated = truncated
}

func (state *transportRequestState) providerError() (string, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.providerErrorBody, state.providerErrorBodyTruncated
}

func (state *transportRequestState) setResponseBody(body *boundedSSEBody) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.responseBody = body
}

func (state *transportRequestState) closeResponseBody() {
	state.mu.Lock()
	body := state.responseBody
	state.mu.Unlock()
	if body != nil {
		_ = body.Close()
	}
}

func (state *transportRequestState) responseLimitReached() bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	body := state.responseBody
	state.mu.Unlock()
	return body != nil && errors.Is(body.failure, errModelResponseLimitReached)
}

type guardedRoundTripper struct {
	base       http.RoundTripper
	origin     *url.URL
	credential *config.SecretValue
}

func (transport *guardedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	state, ok := request.Context().Value(transportRequestStateKey{}).(*transportRequestState)
	if !ok {
		return nil, errTransportRequestInvalid
	}
	state.markTransportEntered()
	if request != nil && request.ContentLength > int64(domain.MaxModelRequestBytes) {
		return nil, errModelRequestLimitReached
	}
	if !transport.validRequest(request) {
		return nil, errTransportRequestInvalid
	}
	if useError := transport.credential.Use(func(value string) {
		request.Header.Set("Authorization", "Bearer "+value)
	}); useError != nil {
		return nil, errTransportRequestInvalid
	}
	response, err := transport.base.RoundTrip(request)
	request.Header.Set("Authorization", "Bearer "+einoCredentialPlaceholder)
	if response != nil {
		state.setHTTPStatus(response.StatusCode)
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, errTransportRequestInvalid
	}
	if response.StatusCode != http.StatusOK {
		sanitizeProviderErrorResponse(response, state)
		return response, nil
	}
	mediaType, _, mediaError := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaError != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		_ = response.Body.Close()
		return nil, errUnsupportedResponseMedia
	}
	if requestID := response.Header.Get("X-Request-ID"); requestID != "" &&
		(!validProviderRequestIDText(requestID) || credentialAppearsInStrings(transport.credential, requestID)) {
		_ = response.Body.Close()
		return nil, errMalformedProviderChunk
	}
	response.Header.Del("X-Request-ID")
	boundedBody := &boundedSSEBody{ReadCloser: response.Body}
	state.setResponseBody(boundedBody)
	response.Body = boundedBody
	return response, nil
}

func (transport *guardedRoundTripper) validRequest(request *http.Request) bool {
	if transport == nil || transport.base == nil || transport.origin == nil || request == nil || request.URL == nil ||
		request.Method != http.MethodPost || request.Body == nil || request.ContentLength < 0 ||
		request.ContentLength > int64(domain.MaxModelRequestBytes) ||
		request.URL.Scheme != transport.origin.Scheme || request.URL.Host != transport.origin.Host ||
		request.URL.User != nil || request.URL.RawQuery != "" || request.URL.ForceQuery || request.URL.Fragment != "" ||
		request.Header.Get("Authorization") != "Bearer "+einoCredentialPlaceholder {
		return false
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && contentType == "application/json"
}

func sanitizeProviderErrorResponse(response *http.Response, state *transportRequestState) {
	if state != nil && state.sensitiveDiagnostics {
		body, _ := io.ReadAll(io.LimitReader(response.Body, int64(domain.MaxModelErrorBodyBytes+1)))
		truncated := len(body) > domain.MaxModelErrorBodyBytes
		if truncated {
			body = body[:domain.MaxModelErrorBodyBytes]
		}
		state.setProviderError(body, truncated)
		for index := range body {
			body[index] = 0
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, int64(domain.MaxModelErrorBodyBytes)))
	}
	_ = response.Body.Close()
	body := []byte(safeProviderErrorBody)
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Type", "application/json")
	response.Header.Del("Content-Length")
}

type boundedSSEBody struct {
	io.ReadCloser
	closeOnce        sync.Once
	closeError       error
	totalBytes       int
	dataEvents       int
	lineBytes        int
	linePrefix       [5]byte
	linePrefixBytes  int
	dataLine         bool
	dataLeadingSpace bool
	failure          error
}

func (body *boundedSSEBody) Close() error {
	body.closeOnce.Do(func() {
		body.closeError = body.ReadCloser.Close()
	})
	return body.closeError
}

func (body *boundedSSEBody) Read(target []byte) (int, error) {
	if body.failure != nil {
		return 0, body.failure
	}
	remaining := domain.MaxModelStreamBytes - body.totalBytes + 1
	if remaining < len(target) {
		target = target[:remaining]
	}
	count, readError := body.ReadCloser.Read(target)
	for index := 0; index < count; index++ {
		if !body.accept(target[index]) {
			body.failure = errModelResponseLimitReached
			return index, body.failure
		}
	}
	return count, readError
}

func (body *boundedSSEBody) accept(current byte) bool {
	body.totalBytes++
	if body.totalBytes > domain.MaxModelStreamBytes {
		return false
	}
	if current == '\n' {
		body.resetLine()
		return true
	}
	body.lineBytes++
	if body.linePrefixBytes < len(body.linePrefix) {
		body.linePrefix[body.linePrefixBytes] = current
		body.linePrefixBytes++
		if body.linePrefixBytes == len(body.linePrefix) && string(body.linePrefix[:]) == "data:" {
			body.dataLine = true
			body.dataEvents++
			if body.dataEvents > domain.MaxModelStreamChunks {
				return false
			}
		}
		return true
	}
	if !body.dataLine {
		return true
	}
	if body.lineBytes == len(body.linePrefix)+1 && current == ' ' {
		body.dataLeadingSpace = true
	}
	payloadBytes := body.lineBytes - len(body.linePrefix)
	if body.dataLeadingSpace {
		payloadBytes--
	}
	return payloadBytes <= domain.MaxModelStreamChunkBytes
}

func (body *boundedSSEBody) resetLine() {
	body.lineBytes = 0
	body.linePrefixBytes = 0
	body.dataLine = false
	body.dataLeadingSpace = false
}

func contextModelErrorCode(ctx context.Context) (domain.ModelErrorCode, bool) {
	switch {
	case errorsIsContext(ctx, context.DeadlineExceeded):
		return domain.ModelErrorCodeTimeout, true
	case errorsIsContext(ctx, context.Canceled):
		return domain.ModelErrorCodeCancelled, true
	default:
		return "", false
	}
}

func errorsIsContext(ctx context.Context, target error) bool {
	return ctx != nil && (errors.Is(context.Cause(ctx), target) || errors.Is(ctx.Err(), target))
}
