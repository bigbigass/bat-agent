package main

import (
	"errors"
	"net/http"

	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func runErrorStatus(err error) (string, bool) {
	var httpErr apiclient.HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusBadRequest:
			return "请求错误: " + httpErr.Error(), false
		case http.StatusUnauthorized:
			return "鉴权失败: " + httpErr.Error(), false
		case http.StatusNotFound:
			return "脚本不存在: " + httpErr.Error(), true
		case http.StatusConflict:
			return "脚本正在运行: " + httpErr.Error(), false
		}
	}
	return "请求失败: " + err.Error(), false
}
