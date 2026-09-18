package httpapi

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/channelbridge"
)

const maxChannelCallbackBodyBytes = 1 << 20

type wecomCallbackEnvelope struct {
	Encrypt string `xml:"Encrypt"`
}

// chatwootBridgeWebhook receives only server-to-server Chatwoot deliveries.
// It verifies the raw body before decoding it and returns 5xx for downstream
// failures so Chatwoot can retry without exposing implementation details.
func (s *Server) chatwootBridgeWebhook(w http.ResponseWriter, r *http.Request) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("chatwoot webhook received", "has_signature", r.Header.Get("X-Chatwoot-Signature") != "", "has_timestamp", r.Header.Get("X-Chatwoot-Timestamp") != "")
	if s.channelBridge == nil {
		logger.Warn("chatwoot webhook rejected", "reason", "bridge_disabled")
		writeError(w, http.StatusNotFound, "CHANNEL_BRIDGE_DISABLED", "客服通道未启用")
		return
	}
	body, err := readChannelCallbackBody(r)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体过大")
		return
	}
	timestamp := r.Header.Get("X-Chatwoot-Timestamp")
	signature := r.Header.Get("X-Chatwoot-Signature")
	if !channelbridge.VerifyWebhookSignature(string(body), timestamp, signature, s.channelBridge.WebhookSecret(), time.Now(), 0) {
		logger.Warn("chatwoot webhook rejected", "reason", "signature_invalid")
		writeError(w, http.StatusUnauthorized, "WEBHOOK_SIGNATURE_INVALID", "Webhook 签名无效")
		return
	}
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求格式不正确")
		return
	}
	if err := s.channelBridge.HandleChatwootWebhook(r.Context(), string(body), timestamp, signature); err != nil {
		logger.Error("chatwoot bridge failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "CHANNEL_BRIDGE_UNAVAILABLE", "客服通道暂时不可用")
		return
	}
	logger.Info("chatwoot webhook processed", "status", http.StatusNoContent)
	w.WriteHeader(http.StatusNoContent)
}

// wecomCallback handles both Agent callback URL verification (GET) and
// encrypted event delivery (POST). Valid but irrelevant messages are
// acknowledged so WeCom does not retry them indefinitely; downstream errors
// return 5xx and are eligible for provider retry.
func (s *Server) wecomCallback(w http.ResponseWriter, r *http.Request) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	query := r.URL.Query()
	logger.Info("wecom callback received", "method", r.Method,
		"has_signature", query.Get("msg_signature") != "",
		"has_timestamp", query.Get("timestamp") != "",
		"has_nonce", query.Get("nonce") != "",
		"has_echo", query.Get("echostr") != "")
	if s.channelBridge == nil {
		logger.Warn("wecom callback rejected", "reason", "bridge_disabled")
		writeError(w, http.StatusNotFound, "CHANNEL_BRIDGE_DISABLED", "客服通道未启用")
		return
	}
	client := s.channelBridge.WeComClient()
	if client == nil {
		writeError(w, http.StatusServiceUnavailable, "CHANNEL_BRIDGE_UNAVAILABLE", "客服通道暂时不可用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		plain, err := client.VerifyEcho(
			r.URL.Query().Get("msg_signature"),
			r.URL.Query().Get("timestamp"),
			r.URL.Query().Get("nonce"),
			r.URL.Query().Get("echostr"),
		)
		if err != nil {
			logger.Warn("wecom callback verification failed", "reason", err.Error())
			writeError(w, http.StatusUnauthorized, "WECOM_SIGNATURE_INVALID", "企业微信回调验证失败")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, plain)
		return
	case http.MethodPost:
		body, err := readChannelCallbackBody(r)
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体过大")
			return
		}
		var envelope wecomCallbackEnvelope
		if err := xml.Unmarshal(body, &envelope); err != nil || strings.TrimSpace(envelope.Encrypt) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_WECOM_CALLBACK", "企业微信回调格式不正确")
			return
		}
		message, err := client.DecryptCallback(
			r.URL.Query().Get("msg_signature"),
			r.URL.Query().Get("timestamp"),
			r.URL.Query().Get("nonce"),
			envelope.Encrypt,
		)
		if err != nil {
			logger.Warn("wecom callback rejected", "reason", "decrypt_failed", "error", err.Error())
			writeError(w, http.StatusUnauthorized, "WECOM_SIGNATURE_INVALID", "企业微信回调验证失败")
			return
		}
		if err := s.channelBridge.HandleWeComMessage(r.Context(), message); err != nil {
			// Unknown operators, unsupported message types, and replies without
			// an explicit conversation reference are intentionally ignored.
			if isIgnoredWeComMessageError(err) {
				logger.Warn("wecom reply ignored", "reason", err.Error())
				writeWeComSuccess(w)
				return
			}
			logger.Error("wecom reply forwarding failed", "error", err.Error())
			writeError(w, http.StatusServiceUnavailable, "CHANNEL_BRIDGE_UNAVAILABLE", "客服通道暂时不可用")
			return
		}
		logger.Info("wecom callback processed", "status", http.StatusOK)
		writeWeComSuccess(w)
		return
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不支持")
	}
}

func readChannelCallbackBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChannelCallbackBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxChannelCallbackBodyBytes {
		return nil, errors.New("channel callback body exceeds size limit")
	}
	return body, nil
}

func writeWeComSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "success")
}

func isIgnoredWeComMessageError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "sender is not the configured operator") ||
		strings.Contains(message, "must include FP-CW:") ||
		strings.Contains(message, "conversation reference is invalid")
}
