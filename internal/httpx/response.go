// Package httpx 统一 HTTP 响应与错误格式。
//
// 所有接口的错误响应都是 {"error":{"code":"...","message":"..."}}，
// 客户端只需实现一套错误解析逻辑。
package httpx

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 错误码。与《Go后端设计.md》3.1.2 的错误码表一一对应。
const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeInvalidCredentials = "INVALID_CREDENTIALS"
	CodeTokenExpired       = "TOKEN_EXPIRED"
	CodeTokenInvalid       = "TOKEN_INVALID"
	CodeUsernameTaken      = "USERNAME_TAKEN"
	CodeTooManyRequests    = "TOO_MANY_REQUESTS"
	CodeInternalError      = "INTERNAL_ERROR"

	// 房间模块。
	CodeRoomNotFound = "ROOM_NOT_FOUND"
	CodeRoomConflict = "ROOM_CONFLICT"
	CodeForbidden    = "FORBIDDEN"

	// 资产快照模块。
	CodeSchemaVersionUnsupported = "SCHEMA_VERSION_UNSUPPORTED"
	CodePayloadTooLarge          = "PAYLOAD_TOO_LARGE"
)

// ErrorBody 错误响应体。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorResponse 错误响应外层。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// Fail 写出统一格式的错误响应。
func Fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{
		Error: ErrorBody{Code: code, Message: message},
	})
}

// BadRequest 参数格式错误。
func BadRequest(c *gin.Context, message string) {
	Fail(c, http.StatusBadRequest, CodeInvalidRequest, message)
}

// Unauthorized 未认证或凭证错误。
func Unauthorized(c *gin.Context, code, message string) {
	Fail(c, http.StatusUnauthorized, code, message)
}

// Conflict 资源冲突（如用户名已存在）。
func Conflict(c *gin.Context, code, message string) {
	Fail(c, http.StatusConflict, code, message)
}

// TooManyRequests 触发限流。
func TooManyRequests(c *gin.Context, message string) {
	Fail(c, http.StatusTooManyRequests, CodeTooManyRequests, message)
}

// Internal 服务端内部错误。对外只给通用文案，细节写日志。
func Internal(c *gin.Context, message string) {
	Fail(c, http.StatusInternalServerError, CodeInternalError, message)
}
