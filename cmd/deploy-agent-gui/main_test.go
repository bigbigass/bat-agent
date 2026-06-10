package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/liqixin/deploy-agent/internal/gui/apiclient"
)

func TestCapOutputKeepsTailAndMarksTruncation(t *testing.T) {
	tail := "tail output"
	got := capOutput(strings.Repeat("x", maxOutputRunes) + tail)

	if !strings.HasPrefix(got, outputTruncatedNotice) {
		t.Fatalf("output prefix = %q, want truncation notice", got[:len(outputTruncatedNotice)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("output suffix = %q, want %q", got[len(got)-len(tail):], tail)
	}
	if utf8.RuneCountInString(got) > maxOutputRunes {
		t.Fatalf("output length = %d, want <= %d", utf8.RuneCountInString(got), maxOutputRunes)
	}
}

func TestRunErrorStatusRefreshesOnNotFound(t *testing.T) {
	status, refresh := runErrorStatus(apiclient.HTTPError{StatusCode: http.StatusNotFound, Message: "script not found"})

	if !refresh {
		t.Fatal("refresh = false, want true")
	}
	if !strings.Contains(status, "脚本不存在") {
		t.Fatalf("status = %q, want script-not-found message", status)
	}
}

func TestRunErrorStatusMapsCommonHTTPErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       string
	}{
		{name: "bad request", statusCode: http.StatusBadRequest, want: "请求错误"},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, want: "鉴权失败"},
		{name: "conflict", statusCode: http.StatusConflict, want: "脚本正在运行"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, refresh := runErrorStatus(apiclient.HTTPError{StatusCode: tt.statusCode, Message: "message"})
			if refresh {
				t.Fatal("refresh = true, want false")
			}
			if !strings.Contains(status, tt.want) {
				t.Fatalf("status = %q, want it to contain %q", status, tt.want)
			}
		})
	}
}

func TestRunErrorStatusGenericError(t *testing.T) {
	status, refresh := runErrorStatus(errors.New("network down"))

	if refresh {
		t.Fatal("refresh = true, want false")
	}
	if !strings.Contains(status, "请求失败") {
		t.Fatalf("status = %q, want generic request failure", status)
	}
}
